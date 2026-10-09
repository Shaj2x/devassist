import { useMemo, useState } from "react";
import { displayPath, parseDiff, type FileDiff } from "../lib/diff";
import { cx } from "../lib/ui";

export function DiffView({ diff }: { diff: string }) {
  const files = useMemo(() => parseDiff(diff), [diff]);
  if (files.length === 0) return <p className="text-sm text-slate-500">This patch is empty.</p>;
  return (
    <div className="space-y-4">
      {files.map((file, i) => (
        <FileBlock key={`${displayPath(file)}-${String(i)}`} file={file} />
      ))}
    </div>
  );
}

function FileBlock({ file }: { file: FileDiff }) {
  const [open, setOpen] = useState(true);
  const tag = file.oldPath === null ? "new" : file.newPath === null ? "deleted" : null;
  return (
    <div className="overflow-hidden rounded-md border border-slate-200">
      <button
        type="button"
        onClick={() => {
          setOpen(!open);
        }}
        aria-expanded={open}
        className="flex w-full items-center gap-3 bg-slate-50 px-3 py-2 text-left font-mono text-xs hover:bg-slate-100"
      >
        <span className="text-slate-400">{open ? "▾" : "▸"}</span>
        <span className="truncate font-medium text-slate-800">{displayPath(file)}</span>
        {tag && <span className="rounded bg-slate-200 px-1.5 text-[10px] uppercase text-slate-600">{tag}</span>}
        <span className="ml-auto whitespace-nowrap">
          <span className="text-emerald-700">+{file.additions}</span>{" "}
          <span className="text-red-700">−{file.deletions}</span>
        </span>
      </button>
      {open && (
        <div className="overflow-x-auto">
          {file.binary ? (
            <p className="px-3 py-2 text-xs text-slate-500">Binary file changed.</p>
          ) : (
            <table className="w-full border-collapse font-mono text-xs leading-5">
              <tbody>
                {file.hunks.map((hunk, h) => (
                  <HunkRows key={h} header={hunk.header} lines={hunk.lines} />
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  );
}

function HunkRows({ header, lines }: { header: string; lines: FileDiff["hunks"][number]["lines"] }) {
  return (
    <>
      <tr className="bg-sky-50 text-sky-800">
        <td colSpan={3} className="px-3 py-0.5">
          {header}
        </td>
      </tr>
      {lines.map((line, i) => (
        <tr
          key={i}
          data-kind={line.kind}
          className={cx(line.kind === "add" && "bg-emerald-50", line.kind === "del" && "bg-red-50")}
        >
          <td className="w-10 select-none border-r border-slate-100 px-2 text-right text-slate-400">
            {line.oldNo ?? ""}
          </td>
          <td className="w-10 select-none border-r border-slate-100 px-2 text-right text-slate-400">
            {line.newNo ?? ""}
          </td>
          <td className="whitespace-pre px-3 text-slate-800">
            <span
              className={cx(
                "mr-2 select-none",
                line.kind === "add" ? "text-emerald-700" : line.kind === "del" ? "text-red-700" : "text-slate-300",
              )}
            >
              {line.kind === "add" ? "+" : line.kind === "del" ? "−" : " "}
            </span>
            {line.text}
          </td>
        </tr>
      ))}
    </>
  );
}
