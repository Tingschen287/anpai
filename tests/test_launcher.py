import importlib.util
import os
from pathlib import Path
import tempfile
import tomllib
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "src" / "ai_launcher.py"
SPEC = importlib.util.spec_from_file_location("ai_launcher", SOURCE)
launcher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(launcher)


class FakeTerm:
    def __init__(self, *keys):
        self.keys = iter(keys)

    def key(self):
        return "key", next(self.keys)


class EventTerm:
    def __init__(self, *events):
        self.events = iter(events)

    def key(self):
        return next(self.events)


class SequenceTerm:
    def __init__(self, chars):
        self.chars = iter(chars)

    def _getch(self, timeout=None):
        return next(self.chars, None)


class LauncherTests(unittest.TestCase):
    def test_example_config_is_valid_and_has_unique_keys(self):
        with (ROOT / "config" / "agents.example.toml").open("rb") as handle:
            config = tomllib.load(handle)
        agents = config["agent"]
        keys = [agent["key"] for agent in agents]
        self.assertEqual(len(keys), len(set(keys)))
        self.assertEqual(keys, ["cco", "ccs", "codex", "grok", "grokt", "kimi", "opencode"])

    def test_load_agents_accepts_example_config(self):
        old_config = launcher.CONF
        launcher.CONF = str(ROOT / "config" / "agents.example.toml")
        try:
            agents = launcher.load_agents()
        finally:
            launcher.CONF = old_config
        self.assertEqual(agents[0]["cmd"], "claude")
        self.assertEqual(agents[-1]["cmd"], "opencode")
        self.assertEqual(agents[-1]["color"], "#ff5c00")

    def test_empty_key_does_not_crash_agent_picker(self):
        agents = [{"key": "x", "name": "X", "color": "#ffffff", "note": ""}]
        with patch.object(launcher, "draw", return_value={"rows": {}}):
            result = launcher.pick_agent(FakeTerm("", "q"), agents)
        self.assertIsNone(result)

    def test_empty_key_does_not_crash_directory_picker(self):
        with tempfile.TemporaryDirectory() as directory:
            item = {"path": directory, "ts": None}
            agent = {
                "key": "x",
                "name": "X",
                "color": "#ffffff",
                "default_dir": directory,
            }
            geometry = {"rows": {}, "bottom": 1, "left": 0}
            with patch.object(launcher, "dir_candidates", return_value=[item]), \
                    patch.object(launcher, "fill_git"), \
                    patch.object(launcher, "draw", return_value=geometry):
                result = launcher.pick_dir(FakeTerm("", "\r"), agent, allow_back=False)
            self.assertEqual(result, (directory, False))

    def test_directory_picker_can_resume_selected_path(self):
        with tempfile.TemporaryDirectory() as directory:
            item = {"path": directory, "ts": None}
            agent = {
                "key": "codex",
                "name": "Codex",
                "color": "#ffffff",
                "default_dir": directory,
                "resume_args": ["resume"],
            }
            geometry = {"rows": {}, "bottom": 1, "left": 0}
            with patch.object(launcher, "dir_candidates", return_value=[item]), \
                    patch.object(launcher, "fill_git"), \
                    patch.object(launcher, "draw", return_value=geometry):
                result = launcher.pick_dir(FakeTerm("r", "\r"), agent, allow_back=False)
            self.assertEqual(result, (directory, True))

    def test_directory_picker_resume_tab_is_clickable(self):
        with tempfile.TemporaryDirectory() as directory:
            item = {"path": directory, "ts": None}
            agent = {
                "key": "codex",
                "name": "Codex",
                "color": "#ffffff",
                "default_dir": directory,
                "resume_args": ["resume"],
            }
            geometry = {
                "rows": {10: 0},
                "tabs": [{"row": 5, "start": 20, "end": 27, "mode": "resume"}],
                "left": 0,
                "width": 40,
            }
            events = EventTerm(
                ("mouse", 5, 22, "click"),
                ("mouse", 10, 5, "click"),
            )
            with patch.object(launcher, "dir_candidates", return_value=[item]), \
                    patch.object(launcher, "fill_git"), \
                    patch.object(launcher, "draw", return_value=geometry):
                result = launcher.pick_dir(events, agent, allow_back=False)
            self.assertEqual(result, (directory, True))

    def test_resume_mode_defaults_to_new(self):
        with tempfile.TemporaryDirectory() as directory:
            item = {"path": directory, "ts": None}
            agent = {
                "key": "codex",
                "name": "Codex",
                "color": "#ffffff",
                "default_dir": directory,
                "resume_args": ["resume"],
            }
            geometry = {"rows": {}, "bottom": 1, "left": 0}
            with patch.object(launcher, "dir_candidates", return_value=[item]), \
                    patch.object(launcher, "fill_git"), \
                    patch.object(launcher, "draw", return_value=geometry):
                result = launcher.pick_dir(FakeTerm("\r"), agent, allow_back=False)
            self.assertEqual(result, (directory, False))

    def test_sgr_mouse_motion_is_reported_as_hover_event(self):
        sequence = [launcher.ESC, "["] + list("<35;12;7M")
        event = launcher.Term.key(SequenceTerm(sequence))
        self.assertEqual(event, ("mouse", 7, 12, "move"))

    def test_agent_picker_renders_hovered_row(self):
        agents = [
            {"key": "a", "name": "A", "color": "#ffffff", "note": ""},
            {"key": "b", "name": "B", "color": "#ffffff", "note": ""},
        ]
        geometry = {"rows": {10: 1}, "left": 0, "width": 20}
        events = EventTerm(("mouse", 10, 5, "move"), ("key", "q"))
        with patch.object(launcher, "draw", return_value=geometry) as draw:
            result = launcher.pick_agent(events, agents)
        self.assertIsNone(result)
        self.assertEqual(draw.call_args_list[1].args[3], 1)

    def test_build_script_passes_arguments_to_native_cli(self):
        agent = {
            "cmd": "codex",
            "proxy": False,
            "path_prepend": [],
            "unset": [],
            "env": {},
        }
        script = launcher.build_script(agent, "/tmp/project")
        self.assertIn('codex "$@"', script)
        self.assertIn("cd /tmp/project", script)
        self.assertIn("export WSL_PROXY_AUTO=0", script)
        self.assertIn("unset HTTP_PROXY HTTPS_PROXY", script)
        self.assertNotIn("then proxy-on --quiet", script)

    def test_build_script_explicitly_enables_proxy_when_configured(self):
        agent = {
            "cmd": "codex",
            "proxy": True,
            "path_prepend": [],
            "unset": [],
            "env": {},
        }
        script = launcher.build_script(agent, "/tmp/project")
        self.assertIn("export WSL_PROXY_AUTO=0", script)
        self.assertIn("then proxy-on --quiet", script)

    def test_build_script_uses_agent_specific_resume_arguments(self):
        agent = {
            "cmd": "codex",
            "resume_args": ["resume"],
            "proxy": True,
            "path_prepend": [],
            "unset": [],
            "env": {},
        }
        script = launcher.build_script(agent, "/tmp/project", resume=True)
        self.assertIn('codex resume "$@"', script)

    def test_direct_resume_cli_reaches_launch_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            agent = {
                "key": "codex",
                "name": "Codex",
                "cmd": "codex",
                "resume_args": ["resume"],
            }
            argv = ["ai", "--resume", "codex", directory]
            with patch.object(launcher, "load_agents", return_value=[agent]), \
                    patch.object(launcher.sys, "argv", argv), \
                    patch.object(launcher, "launch") as launch:
                launcher.main()
            launch.assert_called_once_with(agent, directory, [], True)

    def test_cc_switch_example_is_direct(self):
        with (ROOT / "config" / "agents.example.toml").open("rb") as handle:
            config = tomllib.load(handle)
        agents = {agent["key"]: agent for agent in config["agent"]}
        self.assertFalse(agents["ccs"]["proxy"])

    def test_example_config_has_correct_resume_commands(self):
        with (ROOT / "config" / "agents.example.toml").open("rb") as handle:
            config = tomllib.load(handle)
        actual = {agent["key"]: agent["resume_args"] for agent in config["agent"]}
        self.assertEqual(actual, {
            "cco": ["--resume"],
            "ccs": ["--resume"],
            "codex": ["resume"],
            "grok": ["/resume"],
            "grokt": ["/resume"],
            "kimi": ["--session"],
            "opencode": ["--continue"],
        })

    def test_path_suggestions_filter_directories_and_preserve_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Alpha").mkdir()
            (root / "beta").mkdir()
            (root / ".hidden").mkdir()
            (root / "artifact.txt").write_text("not a directory")
            suggestions = launcher.path_suggestions(f"{directory}/a")
        self.assertEqual([item["name"] for item in suggestions], ["Alpha"])
        self.assertEqual(suggestions[0]["input"], f"{directory}/Alpha/")

    def test_path_suggestions_hide_dot_directories_until_requested(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".hidden").mkdir()
            (root / "visible").mkdir()
            regular = launcher.path_suggestions(f"{directory}/")
            hidden = launcher.path_suggestions(f"{directory}/.")
        self.assertEqual([item["name"] for item in regular], ["visible"])
        self.assertEqual([item["name"] for item in hidden], [".hidden"])

    def test_path_picker_can_drill_down_with_tab(self):
        with tempfile.TemporaryDirectory() as directory:
            child = Path(directory) / "alpha" / "child"
            child.mkdir(parents=True)
            geometry = {"rows": {}, "bottom": 1, "left": 0}
            with patch.object(launcher, "draw", return_value=geometry):
                result = launcher.pick_path(
                    FakeTerm("\t", "down", "\r"),
                    {"key": "x", "name": "X", "color": "#ffffff"},
                    f"{directory}/a",
                )
            self.assertEqual(result, str(child))

    def test_status_uses_agent_proxy_policy(self):
        with patch.object(launcher.os.path, "exists", return_value=True), \
                patch.dict(launcher.os.environ, {"WSL_DISTRO_NAME": "TestLinux"}):
            self.assertEqual(launcher.status_right(), "TestLinux · proxy-on ✓")
            self.assertEqual(
                launcher.status_right({"proxy": True}),
                "TestLinux · 代理 ✓",
            )
            self.assertEqual(
                launcher.status_right({"proxy": False}),
                "TestLinux · 直连",
            )


if __name__ == "__main__":
    unittest.main()
