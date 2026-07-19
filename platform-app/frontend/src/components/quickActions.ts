export type QuickActionMessageInput = {
  id: string;
};

export function messageForQuickAction(action: QuickActionMessageInput): string {
  return `__action:${action.id}`;
}
