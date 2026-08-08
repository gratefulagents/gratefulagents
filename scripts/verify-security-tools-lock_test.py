import importlib.util
import json
import tempfile
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).with_name("verify-security-tools-lock.py")
SPEC = importlib.util.spec_from_file_location("verify_security_tools_lock", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)
LOCK_PATH = MODULE_PATH.parent.parent / "security-tools.lock.json"


class VerifySecurityToolsLockTest(unittest.TestCase):
    def load_lock(self):
        return json.loads(LOCK_PATH.read_text(encoding="utf-8"))

    def verify(self, lock):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory, "lock.json")
            path.write_text(json.dumps(lock), encoding="utf-8")
            MODULE.verify_lock(path)

    def test_repository_lock_is_valid(self):
        self.verify(self.load_lock())

    def test_rejects_missing_enabled_tool_digest(self):
        lock = self.load_lock()
        lock["tools"][0]["platforms"]["linux/amd64"]["sha256"] = ""
        with self.assertRaisesRegex(MODULE.LockError, "sha256 must be a non-empty string"):
            self.verify(lock)

    def test_rejects_unverifiable_digest_format(self):
        lock = self.load_lock()
        lock["tools"][0]["platforms"]["linux/amd64"]["sha256"] = "0" * 63
        with self.assertRaisesRegex(MODULE.LockError, "lowercase SHA-256 digest"):
            self.verify(lock)

    def test_rejects_invalid_extracted_binary_digest(self):
        lock = self.load_lock()
        nuclei = next(tool for tool in lock["tools"] if tool["name"] == "nuclei")
        nuclei["platforms"]["linux/amd64"]["binary_sha256"] = "A" * 64
        with self.assertRaisesRegex(MODULE.LockError, "binary_sha256 must be a lowercase SHA-256 digest"):
            self.verify(lock)

    def test_rejects_asset_that_does_not_identify_the_pinned_version(self):
        lock = self.load_lock()
        lock["tools"][0]["platforms"]["linux/amd64"]["asset"] = "https://example.test/gitleaks.tar.gz"
        with self.assertRaisesRegex(MODULE.LockError, "asset must identify the pinned version"):
            self.verify(lock)

    def test_rejects_disabled_tool_without_reason(self):
        lock = self.load_lock()
        lock["tools"][-1].pop("reason")
        with self.assertRaisesRegex(MODULE.LockError, "reason must be a non-empty string"):
            self.verify(lock)

    def test_rejects_disabled_tool_with_install_fields(self):
        lock = self.load_lock()
        lock["tools"][-1]["version"] = "1.0.0"
        with self.assertRaisesRegex(MODULE.LockError, "disabled tools may not define install fields"):
            self.verify(lock)


if __name__ == "__main__":
    unittest.main()
