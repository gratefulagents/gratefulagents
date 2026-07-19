import { SoulSection } from "@/components/SoulSection";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";

/** /settings/soul — the user's personal agent persona (SOUL). */
export default function SoulPage() {
  return (
    <SettingsSubPage
      title="SOUL"
      description="Your agent persona — teammates can ask it for your perspective."
    >
      <SoulSection />
    </SettingsSubPage>
  );
}
