// AWX's LOCAL_PASSWORD_MIN_* rules, as the server checks them: length,
// digits, uppercase characters, then special characters (anything not a
// letter or digit), with the server's messages.

export interface PasswordRules {
  length: number
  digits: number
  upper: number
  special: number
}

const num = (v: unknown) => (typeof v === 'number' && v > 0 ? v : 0)

export function passwordRules(settings: Record<string, unknown> | undefined): PasswordRules {
  return {
    length: num(settings?.LOCAL_PASSWORD_MIN_LENGTH),
    digits: num(settings?.LOCAL_PASSWORD_MIN_DIGITS),
    upper: num(settings?.LOCAL_PASSWORD_MIN_UPPER),
    special: num(settings?.LOCAL_PASSWORD_MIN_SPECIAL),
  }
}

/** The rules in a sentence, or '' when there are none. */
export function describeRules(r: PasswordRules): string {
  const parts: string[] = []
  if (r.length) parts.push(`${r.length} characters`)
  if (r.digits) parts.push(`${r.digits} ${r.digits === 1 ? 'digit' : 'digits'}`)
  if (r.upper) parts.push(`${r.upper} uppercase`)
  if (r.special) parts.push(`${r.special} special ${r.special === 1 ? 'character' : 'characters'}`)
  if (!parts.length) return ''
  const list = parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}` : parts[0]
  return `At least ${list}.`
}

const count = (s: string, re: RegExp) => [...s].filter((c) => re.test(c)).length

/** The first rule the password breaks, in the server's words, or null. */
export function checkPassword(pw: string, r: PasswordRules): string | null {
  if (r.length && [...pw].length < r.length) return `Password must be at least ${r.length} characters long.`
  if (r.digits && count(pw, /\p{Nd}/u) < r.digits) return `Password must contain at least ${r.digits} digits.`
  if (r.upper && count(pw, /\p{Lu}/u) < r.upper) return `Password must contain at least ${r.upper} uppercase characters.`
  if (r.special && count(pw, /[^\p{L}\p{N}]/u) < r.special) return `Password must contain at least ${r.special} special characters.`
  return null
}
