const localPattern = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/;

function partsAt(date: Date, timeZone: string) {
  const values: Record<string, string> = {};
  for (const part of new Intl.DateTimeFormat("en-CA", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).formatToParts(date)) {
    if (part.type !== "literal") values[part.type] = part.value;
  }
  return values;
}

/** Converts the wall-clock value from datetime-local in the named IANA zone
 * into an absolute instant. Date's string parser always uses the browser's
 * zone, which silently schedules the wrong time when the user selects a
 * different one. Iterating the formatter offset keeps the conversion small
 * without shipping a timezone database to the browser. */
export function zonedLocalToDate(value: string, timeZone: string): Date {
  const match = localPattern.exec(value);
  if (!match) throw new Error("Enter a complete date and time.");
  const target = match.slice(1).map(Number);
  let instant = Date.UTC(target[0], target[1] - 1, target[2], target[3], target[4]);
  try {
    for (let pass = 0; pass < 3; pass += 1) {
      const shown = partsAt(new Date(instant), timeZone);
      const shownAsUTC = Date.UTC(
        Number(shown.year), Number(shown.month) - 1, Number(shown.day),
        Number(shown.hour), Number(shown.minute), Number(shown.second),
      );
      const targetAsUTC = Date.UTC(target[0], target[1] - 1, target[2], target[3], target[4]);
      instant += targetAsUTC - shownAsUTC;
    }
    const roundTrip = dateToZonedLocal(new Date(instant), timeZone);
    if (roundTrip !== value) throw new Error("That local time does not exist in the selected time zone.");
    return new Date(instant);
  } catch (error) {
    if (error instanceof RangeError) throw new Error("Enter a valid IANA time zone, for example Asia/Kolkata.");
    throw error;
  }
}

/** Formats an absolute instant for a datetime-local field in a chosen zone. */
export function dateToZonedLocal(date: Date, timeZone: string): string {
  const value = partsAt(date, timeZone);
  return `${value.year}-${value.month}-${value.day}T${value.hour}:${value.minute}`;
}
