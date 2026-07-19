import { MCPServersSection } from "@/components/MCPServersSection";
import { SettingsSubPage } from "@/components/settings/SettingsSubPage";

/** /settings/mcp — MCP server configs agents can load. */
export default function McpServersPage() {
  return (
    <SettingsSubPage
      title="MCP servers"
      description="MCP server configs agents can load into runs."
    >
      <MCPServersSection />
    </SettingsSubPage>
  );
}
