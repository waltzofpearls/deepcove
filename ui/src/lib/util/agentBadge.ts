// agentBadgeClass colors an agent's badge in its maker's brand, tinted like the other session badges:
// Claude's peach, and Codex in OpenAI's black and white, flipped with the theme. Any other agent gets
// the plain one. The text is a shade the badge's background can read at this size.
export function agentBadgeClass(agent: string): string {
  switch (agent) {
    case 'claude':
      return 'bg-[#DE7356]/10 text-[#B4502F] ring-[#DE7356]/30 dark:bg-[#DE7356]/15 dark:text-[#EFA28C] dark:ring-[#DE7356]/35';
    case 'codex':
      return 'bg-black/5 text-black ring-black/25 dark:bg-white/10 dark:text-white dark:ring-white/35';
    default:
      return 'bg-secondary text-secondary-foreground ring-muted-foreground/30 dark:bg-white/10';
  }
}
