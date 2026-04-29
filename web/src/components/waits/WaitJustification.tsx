import type { ActiveWaitSummary, WaitRecord } from "../../lib/types";

function Field({ label, value, mono = false }: { label: string; value?: string | null; mono?: boolean }) {
  if (!value || !String(value).trim()) {
    return null;
  }
  return (
    <div className="waitField">
      <dt>{label}</dt>
      <dd className={mono ? "font-mono break-all" : undefined}>{value}</dd>
    </div>
  );
}

export function WaitJustification({ wait }: { wait: ActiveWaitSummary | WaitRecord }) {
  const files = "files" in wait && Array.isArray(wait.files) ? wait.files : [];
  return (
    <dl className="waitJustification">
      <Field label="Blocking reason" value={wait.blocking_reason} />
      <Field label="Attempted" value={wait.attempted} />
      <Field label="Default if no reply" value={wait.default_if_no_reply} />
      {files.length ? (
        <div className="waitField">
          <dt>Files</dt>
          <dd>
            <ul className="waitFileList">
              {files.map((file) => <li key={file} className="font-mono break-all">{file}</li>)}
            </ul>
          </dd>
        </div>
      ) : null}
    </dl>
  );
}
