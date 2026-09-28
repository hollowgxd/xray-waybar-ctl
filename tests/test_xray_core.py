import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


core = load("xray_core", ROOT / "scripts/xray-core.py")
watch = load("check_xray_release", ROOT / "scripts/check-xray-release.py")
PIN = json.loads((ROOT / "configs/xray-core-stable.json").read_text())


class XrayCoreTest(unittest.TestCase):
    def test_official_release_digest_and_stable_gate(self):
        tag, name, digest = core.release_from_pin(PIN, "amd64")
        release = {"tag_name": tag, "draft": False, "prerelease": False,
                   "assets": [{"name": name, "digest": "sha256:" + digest}]}
        self.assertEqual(core.release_from_api(release, "amd64"), (tag, name, digest))
        release["prerelease"] = True
        with self.assertRaises(ValueError):
            core.release_from_api(release, "amd64")
        release["prerelease"] = False
        release["assets"][0]["digest"] = "sha256:bad"
        with self.assertRaises(ValueError):
            core.release_from_api(release, "amd64")

    def test_existing_config_migrates_with_backup_and_keeps_subscription(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = Path(tmp) / "app.yaml"
            config.write_text('subscription_url: "https://example.invalid/token"\nxray_bin: "/usr/bin/xray"\n')
            config.chmod(0o600)
            binary = Path(tmp) / "core/xray"
            core.configure(config, binary)
            text = config.read_text()
            self.assertIn('https://example.invalid/token', text)
            self.assertIn(str(binary), text)
            self.assertEqual(config.stat().st_mode & 0o777, 0o600)
            self.assertEqual(len(list(Path(tmp).glob("app.yaml.xray-waybar-backup-*"))), 1)
            core.configure(config, binary)
            self.assertEqual(len(list(Path(tmp).glob("app.yaml.xray-waybar-backup-*"))), 1)

    def test_custom_xray_path_is_preserved(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = Path(tmp) / "app.yaml"
            config.write_text('subscription_url: "u"\nxray_bin: "/opt/my-xray"\n')
            core.configure(config, Path(tmp) / "core/xray")
            self.assertIn('/opt/my-xray', config.read_text())
            self.assertEqual(list(Path(tmp).glob("*backup*")), [])

    def test_release_watcher_ignores_prereleases_and_current_version(self):
        with tempfile.TemporaryDirectory() as tmp:
            release_path = Path(tmp) / "release.json"
            pin_path = ROOT / "configs/xray-core-stable.json"
            release_path.write_text(json.dumps({"tag_name": "v26.9.9", "prerelease": True}))
            self.assertIsNone(watch.main(pin_path, release_path))
            release_path.write_text(json.dumps({"tag_name": PIN["version"], "prerelease": False}))
            self.assertIsNone(watch.main(pin_path, release_path))


if __name__ == "__main__":
    unittest.main()
