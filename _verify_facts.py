import sqlite3, json

conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()

# 1. Verify the latest commit hash - search for recent git commits
sid = "ses_0428c121bffeGgZ7TwTUZSoVSu"
cur.execute("""
    SELECT substr(json_extract(p.data, '$.state.output'), 1, 500) as output
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(p.data, '$.type') = 'tool'
      AND json_extract(p.data, '$.tool') = 'bash'
      AND json_extract(p.data, '$.state.output') LIKE '%git log%'
    ORDER BY m.time_created DESC
    LIMIT 5
""", (sid,))
print("=== Git commits from trajectory ===")
for r in cur.fetchall():
    print(r[0][:400])
    print()

# 2. Check if landing page was actually committed to folio repo
sid2 = "ses_0433b24dcffewgy81gCFs6QhT6"
cur.execute("""
    SELECT substr(json_extract(p.data, '$.state.output'), 1, 500) as output
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(p.data, '$.type') = 'tool'
      AND json_extract(p.data, '$.tool') = 'bash'
    ORDER BY m.time_created ASC
""", (sid2,))
print("=== Landing page session bash outputs ===")
for r in cur.fetchall():
    if r[0]:
        print(r[0][:300])
        print()

# 3. Verify the CI workflow changes
cur.execute("""
    SELECT substr(json_extract(p.data, '$.state.output'), 1, 500) as output
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(p.data, '$.type') = 'tool'
      AND json_extract(p.data, '$.tool') = 'bash'
      AND json_extract(p.data, '$.state.output') LIKE '%ci.yml%'
    ORDER BY m.time_created ASC
    LIMIT 5
""", (sid,))
print("=== CI workflow outputs ===")
for r in cur.fetchall():
    if r[0]:
        print(r[0][:400])
        print()

conn.close()
