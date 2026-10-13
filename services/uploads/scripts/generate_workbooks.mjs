// Fixture workbook writer. Run through generate_fixtures.py so dependencies
// resolve from the bundled Codex runtime, not from application dependencies.
import fs from "node:fs/promises";
import path from "node:path";
import { SpreadsheetFile, Workbook } from "@oai/artifact-tool";

const [specPath, outputDir, previewDir] = process.argv.slice(2);
const specifications = JSON.parse(await fs.readFile(specPath, "utf8"));
await fs.mkdir(previewDir, { recursive: true });
for (const spec of specifications) {
  const book = Workbook.create();
  const sheet = book.worksheets.add(spec.sheet ?? "Fixture");
  const columns = Math.max(...spec.rows.map(row => row.length));
  const rows = spec.rows.map(row => [...row, ...Array(columns - row.length).fill(null)]);
  const grid = sheet.getRangeByIndexes(0, 0, rows.length, columns);
  grid.values = rows;
  grid.format.font = { name: "Arial", size: 10 };
  grid.format.columnWidth = columns > 5 ? 24 : 38;
  grid.format.rowHeight = 22;
  grid.format.verticalAlignment = "center";
  grid.format.wrapText = true;
  sheet.showGridLines = false;
  const headerRow = spec.header_row ?? 1;
  sheet.getRangeByIndexes(headerRow - 1, 0, 1, columns).format = {
    fill: "#334155", font: { name: "Arial", size: 10, bold: true, color: "#FFFFFF" },
    rowHeight: 34, wrapText: true, verticalAlignment: "center",
  };
  if (rows.length > 20) sheet.freezePanes.freezeRows(headerRow);
  for (const address of spec.multiline ?? []) sheet.getRange(address).format.rowHeight = 92;
  for (const [address, formula] of Object.entries(spec.formulas ?? {})) {
    // Only arithmetic formulas are authored. Threat fixture verifies rejection
    // even when a formula has a cached value; no URL/function is executed.
    if (!/^=[0-9+* /()-]+$/.test(formula)) throw new Error("fixture_formula_not_arithmetic");
    sheet.getRange(address).formulas = [[formula]];
  }
  book.recalculate();
  const inspection = await book.inspect({ kind: "table", range: `${sheet.name}!A1:${String.fromCharCode(64 + Math.min(columns, 26))}${Math.min(rows.length, 12)}`, include: "values,formulas", tableMaxRows: 12, tableMaxCols: columns, maxChars: 2000 });
  if (!inspection.ndjson) throw new Error(`empty_inspection:${spec.path}`);
  const scan = await book.inspect({ kind: "match", searchTerm: "#REF!|#DIV/0!|#VALUE!|#NAME\\?|#N/A|#NUM!|#NULL!", options: { useRegex: true, maxResults: 20 }, maxChars: 2000 });
  if (/"cell"/.test(scan.ndjson)) throw new Error(`unexpected_formula_error:${spec.path}`);
  const preview = await book.render({ sheetName: sheet.name, range: `A1:${String.fromCharCode(64 + Math.min(columns, 9))}${Math.min(rows.length, 7)}`, scale: 1, format: "png" });
  await fs.writeFile(path.join(previewDir, spec.path.replaceAll("/", "__").replace(/\.xlsx$/, ".png")), new Uint8Array(await preview.arrayBuffer()));
  const output = path.join(outputDir, spec.path);
  await fs.mkdir(path.dirname(output), { recursive: true });
  await (await SpreadsheetFile.exportXlsx(book)).save(output);
}
console.log(JSON.stringify({ workbooks: specifications.length, inspected: specifications.length, previews: previewDir }));
