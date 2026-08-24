import { useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useMutation } from "@tanstack/react-query";
import type { ImportJob } from "@crm/contracts";
import { PageHeader } from "../../components/shell/PageHeader";
import { Button } from "../../components/ui/Button";
import { Select } from "../../components/ui/Select";
import { Badge } from "../../components/ui/Badge";
import { useToast } from "../../components/ui/Toast";
import { mutationErrorMessage } from "../../lib/connectErrors";
import { useCrmClients } from "../../lib/useCrmClients";

/** The Account fields an import may set — matches the server's own
 * allowlist (internal/domain/import_export.go's importableAccountFields).
 * A CSV header left mapped to "" is ignored. */
const IMPORT_FIELDS: { value: string; label: string }[] = [
  { value: "", label: "Ignore this column" },
  { value: "name", label: "Name (required)" },
  { value: "industry", label: "Industry" },
  { value: "tier", label: "Tier" },
  { value: "owner_id", label: "Owner" },
  { value: "website", label: "Website" },
  { value: "annual_revenue", label: "Annual revenue" },
  { value: "parent_id", label: "Parent account id" },
];

/** The first line of a CSV, split on commas — good enough to offer a
 * mapping UI without pulling in a full CSV parser client-side. The
 * server (encoding/csv) is the real parser; this is only for showing
 * column names before upload. A header containing a quoted comma will
 * show up split incorrectly here, but the mapping the user builds still
 * keys off the header *text*, so as long as they map by what they see,
 * the server parses the same file correctly regardless. */
function splitHeaderLine(content: string): string[] {
  const firstLine = content.split(/\r?\n/, 1)[0] ?? "";
  return firstLine.split(",").map((h) => h.trim()).filter(Boolean);
}

type Stage = "upload" | "mapped" | "previewed" | "committed";

export default function ImportAccountsPage() {
  const { crm } = useCrmClients();
  const navigate = useNavigate();
  const toast = useToast();
  const fileInput = useRef<HTMLInputElement>(null);

  const [stage, setStage] = useState<Stage>("upload");
  const [fileName, setFileName] = useState("");
  const [content, setContent] = useState("");
  const [headers, setHeaders] = useState<string[]>([]);
  const [mapping, setMapping] = useState<Record<string, string>>({});
  const [job, setJob] = useState<ImportJob | null>(null);

  const onFileChosen = (file: File) => {
    const reader = new FileReader();
    reader.onload = () => {
      const text = String(reader.result ?? "");
      setFileName(file.name);
      setContent(text);
      setHeaders(splitHeaderLine(text));
      setMapping({});
      setJob(null);
      setStage("mapped");
    };
    reader.readAsText(file);
  };

  const preview = useMutation({
    mutationFn: () =>
      crm.createImportJob({
        objectType: "account", fileName, content,
        mapping: Object.fromEntries(Object.entries(mapping).filter(([, v]) => v !== "")),
      }),
    onSuccess: (res) => {
      setJob(res.job ?? null);
      setStage("previewed");
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });

  const commit = useMutation({
    mutationFn: () => crm.commitImportJob({ id: job!.id }),
    onSuccess: (res) => {
      setJob(res.job ?? null);
      setStage("committed");
      toast.show({
        tone: "success",
        message: `Imported ${res.job?.createdRows ?? 0} accounts${res.job?.skippedRows ? `, ${res.job.skippedRows} skipped` : ""}.`,
      });
    },
    onError: (err) => toast.show({ tone: "danger", message: mutationErrorMessage(err) }),
  });

  const mappedName = Object.values(mapping).includes("name");

  return (
    <div>
      <PageHeader title="Import accounts" breadcrumbs={[{ label: "Accounts", to: "/accounts" }]} />
      <p className="mb-5 max-w-2xl text-sm text-slate-600">
        Upload a CSV (up to 500 rows, 2&nbsp;MiB), map its columns to account fields, and preview before
        anything is created. Importing never updates or merges an existing account — every row becomes a
        new account, flagged for review the same way a matching website normally is.
      </p>

      {stage === "upload" && (
        <div className="max-w-md rounded-lg border border-dashed border-slate-300 p-8 text-center">
          <input
            ref={fileInput}
            type="file"
            accept=".csv,text/csv"
            aria-label="CSV file to import"
            className="hidden"
            onChange={(e) => e.target.files?.[0] && onFileChosen(e.target.files[0])}
          />
          <p className="mb-3 text-sm text-slate-600">Choose a CSV file with a header row.</p>
          <Button onClick={() => fileInput.current?.click()}>Choose file</Button>
        </div>
      )}

      {stage === "mapped" && (
        <div className="max-w-2xl">
          <p className="mb-3 text-sm text-slate-700">
            <strong>{fileName}</strong> — map each column to an account field.
          </p>
          <div className="grid gap-3 sm:grid-cols-2">
            {headers.map((header) => (
              <Select
                key={header}
                label={header}
                value={mapping[header] ?? ""}
                onChange={(e) => setMapping((prev) => ({ ...prev, [header]: e.target.value }))}
                options={IMPORT_FIELDS}
              />
            ))}
          </div>
          <div className="mt-5 flex gap-2">
            <Button onClick={() => preview.mutate()} loading={preview.isPending} disabled={!mappedName}>
              Preview import
            </Button>
            <Button variant="secondary" onClick={() => setStage("upload")}>
              Choose a different file
            </Button>
          </div>
          {!mappedName && <p className="mt-2 text-sm text-warning-700">Map at least one column to "Name" to continue.</p>}
        </div>
      )}

      {(stage === "previewed" || stage === "committed") && job && (
        <div className="max-w-2xl">
          <div className="mb-4 flex flex-wrap items-center gap-3 rounded-md border border-slate-200 bg-slate-50 px-4 py-3 text-sm">
            <span>
              <strong>{job.rowCount}</strong> row{job.rowCount === 1 ? "" : "s"} read.
            </span>
            {stage === "committed" ? (
              <>
                <Badge tone="success">{job.createdRows} created</Badge>
                {job.skippedRows > 0 && <Badge tone="warning">{job.skippedRows} skipped</Badge>}
              </>
            ) : (
              job.errors.length > 0 && <Badge tone="warning">{job.errors.length} row(s) would be skipped</Badge>
            )}
          </div>

          {job.errors.length > 0 && (
            <div className="mb-4 max-h-64 overflow-y-auto rounded-md border border-slate-200">
              <table className="w-full text-left text-sm">
                <thead className="bg-slate-50 text-xs font-semibold uppercase text-slate-500">
                  <tr>
                    <th className="px-3 py-2">Row</th>
                    <th className="px-3 py-2">Problem</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100">
                  {job.errors.map((e) => (
                    <tr key={e.rowNumber}>
                      <td className="px-3 py-2">{e.rowNumber}</td>
                      <td className="px-3 py-2 text-slate-700">{e.message}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {stage === "previewed" && (
            <div className="flex gap-2">
              <Button onClick={() => commit.mutate()} loading={commit.isPending}>
                Commit import — creates {job.rowCount - job.errors.length} account(s)
              </Button>
              <Button variant="secondary" onClick={() => setStage("upload")}>
                Cancel
              </Button>
            </div>
          )}
          {stage === "committed" && (
            <Button onClick={() => navigate("/accounts")}>Back to accounts</Button>
          )}
        </div>
      )}
    </div>
  );
}
