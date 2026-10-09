import { displayPath, parseDiff } from "./diff";

const DIFF = `diff --git a/datekit/calendar.py b/datekit/calendar.py
index 3b18e51..a9c2f0d 100644
--- a/datekit/calendar.py
+++ b/datekit/calendar.py
@@ -10,4 +10,4 @@ def days_in_month(year, month):
 def is_leap_year(year: int) -> bool:
-    return year % 4 == 0
+    return year % 4 == 0 and (year % 100 != 0 or year % 400 == 0)
 
 
diff --git a/tests/test_new.py b/tests/test_new.py
new file mode 100644
--- /dev/null
+++ b/tests/test_new.py
@@ -0,0 +1,2 @@
+def test_1900():
+    assert not is_leap_year(1900)
\\ No newline at end of file
diff --git a/old.py b/old.py
deleted file mode 100644
--- a/old.py
+++ /dev/null
@@ -1 +0,0 @@
-x = 1
`;

describe("parseDiff", () => {
  const files = parseDiff(DIFF);

  it("splits files and counts changes", () => {
    expect(files.map(displayPath)).toEqual(["datekit/calendar.py", "tests/test_new.py", "old.py"]);
    expect(files.map((f) => [f.additions, f.deletions])).toEqual([
      [1, 1],
      [2, 0],
      [0, 1],
    ]);
  });

  it("numbers old and new lines from the hunk header", () => {
    const lines = files[0]?.hunks[0]?.lines ?? [];
    expect(lines[0]).toEqual({ kind: "context", text: "def is_leap_year(year: int) -> bool:", oldNo: 10, newNo: 10 });
    expect(lines[1]).toMatchObject({ kind: "del", oldNo: 11, newNo: null });
    expect(lines[2]).toMatchObject({ kind: "add", oldNo: null, newNo: 11 });
    expect(lines[3]).toMatchObject({ kind: "context", oldNo: 12, newNo: 12 });
  });

  it("marks created and deleted files", () => {
    expect(files[1]).toMatchObject({ oldPath: null, newPath: "tests/test_new.py" });
    expect(files[1]?.hunks[0]?.lines).toHaveLength(2); // the "\\ No newline" marker is dropped
    expect(files[2]).toMatchObject({ oldPath: "old.py", newPath: null });
  });

  it("accepts a diff without git headers", () => {
    const plain = parseDiff("--- a/x.txt\n+++ b/x.txt\n@@ -1 +1 @@\n-a\n+b\n");
    expect(plain).toHaveLength(1);
    expect(plain[0]).toMatchObject({ oldPath: "x.txt", newPath: "x.txt", additions: 1, deletions: 1 });
  });

  it("returns nothing for an empty diff", () => {
    expect(parseDiff("")).toEqual([]);
  });
});
