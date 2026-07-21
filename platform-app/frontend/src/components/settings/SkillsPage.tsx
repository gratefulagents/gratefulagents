import { SkillsSection } from "@/components/SkillsSection";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";

/** /settings/skills — reusable agent skills, inline or installed from skills.sh. */
export default function SkillsPage() {
  return (
    <SettingsSubPage
      title="Skills"
      description="Reusable agent skills, discoverable in every project and loaded on demand."
    >
      <SkillsSection />
    </SettingsSubPage>
  );
}
