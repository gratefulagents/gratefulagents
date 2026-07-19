import { CredentialsSection } from "@/components/CredentialsSection";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";

/** /settings/credentials — saved API keys and OAuth sign-ins used by runs. */
export default function CredentialsPage() {
  return (
    <SettingsSubPage
      title="Credentials"
      description="API keys and OAuth sign-ins your runs can use."
    >
      <CredentialsSection />
    </SettingsSubPage>
  );
}
