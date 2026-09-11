import type { Lead } from "@crm/contracts";

export function leadPersonName(lead: Pick<Lead, "firstName" | "lastName">): string {
  return `${lead.firstName} ${lead.lastName}`.trim();
}

export function leadDisplayName(
  lead: Pick<Lead, "firstName" | "lastName" | "company" | "contactEmail" | "phone">,
): string {
  return leadPersonName(lead) || lead.company.trim() || lead.contactEmail.trim() || lead.phone.trim() || "Lead";
}

export function isE164Input(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true;
  if (!/^\+[0-9 ()\-./]+$/.test(trimmed)) return false;
  const digits = trimmed.replace(/\D/g, "");
  return digits.length >= 7 && digits.length <= 15;
}
