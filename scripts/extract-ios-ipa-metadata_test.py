import importlib.util
import plistlib
import tempfile
import unittest
import zipfile
from pathlib import Path

MODULE_PATH = Path(__file__).with_name("extract-ios-ipa-metadata.py")
SPEC = importlib.util.spec_from_file_location("extract_ios_ipa_metadata", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class ExtractIOSIPAMetadataTest(unittest.TestCase):
    def make_ipa(self, info, *, extra_app=False, extension=False):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        ipa = Path(temporary.name, "app.ipa")
        with zipfile.ZipFile(ipa, "w") as archive:
            archive.writestr("Payload/gratefulagents.app/Info.plist", plistlib.dumps(info))
            archive.writestr("Payload/gratefulagents.app/gratefulagents", b"not-a-real-mach-o")
            if extra_app:
                archive.writestr("Payload/other.app/Info.plist", plistlib.dumps(info))
            if extension:
                archive.writestr(
                    "Payload/gratefulagents.app/PlugIns/helper.appex/Info.plist",
                    plistlib.dumps(info),
                )
        return ipa

    def make_project(self, *, entitlements=None, capabilities=""):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        project = root / "GratefulAgents.xcodeproj"
        project.mkdir()
        setting = ""
        if entitlements is not None:
            entitlements_path = root / "GratefulAgents" / "GratefulAgents.entitlements"
            entitlements_path.parent.mkdir()
            entitlements_path.write_bytes(plistlib.dumps(entitlements))
            setting = "CODE_SIGN_ENTITLEMENTS = GratefulAgents/GratefulAgents.entitlements;"
        capability_setting = f"SystemCapabilities = {{{capabilities}}};" if capabilities else ""
        (project / "project.pbxproj").write_text(
            f"buildSettings = {{ {setting} }}; {capability_setting}\n",
            encoding="utf-8",
        )
        return root

    def base_info(self):
        return {
            "CFBundleIdentifier": "dev.gratefulagents.app",
            "CFBundleShortVersionString": "1.2.3",
            "CFBundleVersion": "407",
            "MinimumOSVersion": "15.0",
        }

    def test_extracts_exact_bundle_version_os_privacy_and_entitlements(self):
        info = self.base_info()
        info.update(
            {
                "NSCameraUsageDescription": "Scan a workspace code.",
                "NSNotAPermission": "ignored",
            }
        )
        ipa = self.make_ipa(info)
        project = self.make_project(
            entitlements={
                "com.apple.developer.associated-domains": ["applinks:example.com"],
                "com.apple.security.application-groups": ["group.dev.gratefulagents"],
            }
        )

        self.assertEqual(
            MODULE.extract_metadata(ipa, project),
            {
                "schemaVersion": 1,
                "bundleIdentifier": "dev.gratefulagents.app",
                "version": "1.2.3",
                "buildVersion": "407",
                "minOSVersion": "15.0",
                "entitlements": [
                    "com.apple.developer.associated-domains",
                    "com.apple.security.application-groups",
                ],
                "privacy": {"NSCameraUsageDescription": "Scan a workspace code."},
            },
        )

    def test_allows_a_project_with_no_declared_entitlements(self):
        metadata = MODULE.extract_metadata(
            self.make_ipa(self.base_info()),
            self.make_project(),
        )
        self.assertEqual(metadata["entitlements"], [])

    def test_parses_quoted_conditional_and_xcconfig_entitlement_settings(self):
        project = self.make_project(
            entitlements={"com.apple.developer.associated-domains": ["applinks:example.com"]}
        )
        pbxproj = next(project.rglob("project.pbxproj"))
        pbxproj.write_text(
            pbxproj.read_text(encoding="utf-8").replace(
                "CODE_SIGN_ENTITLEMENTS",
                '"CODE_SIGN_ENTITLEMENTS[sdk=iphoneos*]"',
            ),
            encoding="utf-8",
        )
        (project / "Signing.xcconfig").write_text(
            "CODE_SIGN_ENTITLEMENTS = GratefulAgents/GratefulAgents.entitlements\n",
            encoding="utf-8",
        )

        self.assertEqual(
            MODULE.read_project_entitlements(project),
            ["com.apple.developer.associated-domains"],
        )

    def test_fails_closed_for_unparsed_entitlement_settings(self):
        project = self.make_project()
        (project / "Signing.xcconfig").write_text(
            "CODE_SIGN_ENTITLEMENTS ?= unsupported-assignment.entitlements\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(ValueError, "unparsed CODE_SIGN_ENTITLEMENTS"):
            MODULE.read_project_entitlements(project)

    def test_requires_exactly_one_top_level_app(self):
        ipa = self.make_ipa(self.base_info(), extra_app=True)
        with self.assertRaisesRegex(ValueError, "exactly one top-level app Info.plist"):
            MODULE.extract_metadata(ipa, self.make_project())

    def test_rejects_app_extensions_until_their_permissions_are_supported(self):
        ipa = self.make_ipa(self.base_info(), extension=True)
        with self.assertRaisesRegex(ValueError, "contains app extensions"):
            MODULE.extract_metadata(ipa, self.make_project())

    def test_fails_closed_when_the_generated_xcode_project_is_missing(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        missing = Path(temporary.name, "missing")
        with self.assertRaisesRegex(ValueError, "generated Xcode project does not exist"):
            MODULE.extract_metadata(self.make_ipa(self.base_info()), missing)

    def test_fails_closed_for_xcode_generated_capabilities(self):
        project = self.make_project(
            capabilities="com.apple.Push = { enabled = 1; };"
        )
        with self.assertRaisesRegex(ValueError, "declares SystemCapabilities"):
            MODULE.extract_metadata(self.make_ipa(self.base_info()), project)

    def test_rejects_malformed_privacy_usage_descriptions(self):
        info = self.base_info()
        info["NSCameraUsageDescription"] = ""
        with self.assertRaisesRegex(ValueError, "NSCameraUsageDescription"):
            MODULE.extract_metadata(self.make_ipa(info), self.make_project())

    def test_requires_all_version_fields(self):
        info = self.base_info()
        del info["CFBundleVersion"]
        with self.assertRaisesRegex(ValueError, "CFBundleVersion"):
            MODULE.extract_metadata(self.make_ipa(info), self.make_project())


if __name__ == "__main__":
    unittest.main()
