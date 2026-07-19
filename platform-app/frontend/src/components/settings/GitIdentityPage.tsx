import { GitIdentitySection } from "@/components/GitIdentitySection";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";

/** /settings/git — the git identity used to author commits made by the user's runs. */
export default function GitIdentityPage() {
  return (
    <SettingsSubPage
      title="Git identity"
      description="The name and email your runs use to author commits."
    >
      <GitIdentitySection />
    </SettingsSubPage>
  );
}
