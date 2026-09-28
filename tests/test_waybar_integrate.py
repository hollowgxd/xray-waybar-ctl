"""Regression tests for the Waybar patcher; run with python3 -m unittest discover -s tests."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "waybar-integrate.py"
spec = importlib.util.spec_from_file_location("waybar_integrate", SCRIPT)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


class WaybarIntegrationTest(unittest.TestCase):
    def test_new_config_is_strict_json(self):
        result = mod.update_config("{}\n", "/home/user/.local/bin/xray-waybar-ctl", False)
        parsed = json.loads(result)
        self.assertEqual(parsed["modules-left"], ["custom/xray"])
        self.assertEqual(parsed["custom/xray"]["return-type"], "json")

    def test_existing_config_keeps_comments_and_is_idempotent(self):
        source = '{\n  // own comment\n  "modules-left": ["custom/start",],\n  "position": "top",\n}\n'
        result = mod.update_config(source, "/bin/xray-waybar-ctl", False)
        self.assertIn("// own comment", result)
        self.assertEqual(mod.jsonc_value(result)["modules-left"],
                         ["custom/xray", "custom/start"])
        self.assertEqual(mod.update_config(result, "/bin/xray-waybar-ctl", False), result)

    def test_replace_happ_only_in_module_list(self):
        source = '{"modules-left":["custom/start","custom/happ"],"custom/happ":{"exec":"happ"}}'
        result = mod.update_config(source, "/bin/xray-waybar-ctl", True)
        parsed = json.loads(result)
        self.assertEqual(parsed["modules-left"], ["custom/start", "custom/xray"])
        self.assertEqual(parsed["custom/happ"], {"exec": "happ"})

    def test_replace_happ_when_xray_already_present(self):
        source = '{"modules-left":["custom/xray","custom/happ"],"custom/xray":{}}'
        result = mod.update_config(source, "/bin/xray-waybar-ctl", True)
        self.assertEqual(json.loads(result)["modules-left"], ["custom/xray"])

    def test_invalid_config_does_not_write(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "config"
            path.write_text("[not a Waybar object]")
            with self.assertRaises(ValueError):
                mod.update_config(path.read_text(), "/bin/true", False)
            self.assertEqual(path.read_text(), "[not a Waybar object]")

    def test_changed_file_gets_backup_but_repeat_does_not(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "config"
            path.write_text("{}\n")
            self.assertTrue(mod.write_changed(path, '{"foo": 1}\n', False))
            self.assertFalse(mod.write_changed(path, '{"foo": 1}\n', False))
            self.assertEqual(len(list(Path(tmp).glob("config.xray-waybar-backup-*"))), 1)


if __name__ == "__main__":
    unittest.main()
