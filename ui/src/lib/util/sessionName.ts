// sessionNameMaxLength is how many characters a session name shows before it is cut in a narrow header.
export const sessionNameMaxLength = 20;

// shortSessionName cuts a long name in the middle, since names often share a start and differ at the end.
// The name is drawn in a monospace font, so counting characters gives an exact width.
export function shortSessionName(name: string, maxLength = sessionNameMaxLength): string {
  const characters = [...name];
  if (characters.length <= maxLength) return name;
  const tailLength = Math.floor((maxLength - 1) / 2);
  const headLength = maxLength - 1 - tailLength;
  return (
    characters.slice(0, headLength).join('') +
    '…' +
    characters.slice(characters.length - tailLength).join('')
  );
}
