import { RoleModelsSection } from "@/components/RoleModelsSection";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";

export default function RoleModelsPage() {
  return (
    <SettingsSubPage
      title="Role models"
      description="Personal model overrides for platform roles."
    >
      <RoleModelsSection />
    </SettingsSubPage>
  );
}
