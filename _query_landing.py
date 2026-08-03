import sqlite3, json

conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()

# Check the landing page session
sid = "ses_0433b24dcffewgy81gCFs6QhT6"
cur.execute("""
    SELECT m.id, json_extract(m.data, '$.role') as role,
           substr(json_extract(p.data, '$.text'), 1, 400) as text_preview
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(p.data, '$.type') = 'text'
      AND json_extract(p.data, '$.text') NOT LIKE '%system-reminder%'
    ORDER BY m.time_created ASC
""", (sid,))
for r in cur.fetchall():
    print(f"{r[1].upper()} [{r[0]}]: {r[2][:300]}")
    print()

# Check for tool calls (file writes, git commits)
cur.execute("""
    SELECT m.id, json_extract(m.data, '$.role') as role,
           json_extract(p.data, '$.tool') as tool,
           substr(p.data, 1, 500) as preview
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(p.data, '$.type') = 'tool'
    ORDER BY m.time_created ASC
""", (sid,))
for r in cur.fetchall():
    print(f"TOOL [{r[0]}] role={r[1]} tool={r[2]}: {r[3][:200]}")
    print()

conn.close()
