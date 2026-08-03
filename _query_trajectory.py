import sqlite3, json

conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()

# Get user messages from folio session ses_0428c121bffeGgZ7TwTUZSoVSu
# (the one with the cross-device sync fixes)
sid = "ses_0428c121bffeGgZ7TwTUZSoVSu"
cur.execute("""
    SELECT m.id, substr(json_extract(p.data, '$.text'), 1, 500) as text_preview
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(m.data, '$.role') = 'user'
      AND json_extract(p.data, '$.type') = 'text'
      AND json_extract(p.data, '$.text') NOT LIKE '%system-reminder%'
    ORDER BY m.time_created ASC
""", (sid,))
for r in cur.fetchall():
    print(f"USER [{r[0]}]: {r[1][:400]}")
    print()

# Also check assistant messages for key design decisions
cur.execute("""
    SELECT m.id, substr(json_extract(p.data, '$.text'), 1, 500) as text_preview
    FROM message m
    JOIN part p ON p.message_id = m.id
    WHERE m.session_id = ?
      AND json_extract(m.data, '$.role') = 'assistant'
      AND json_extract(p.data, '$.type') = 'text'
      AND (json_extract(p.data, '$.text') LIKE '%decision%'
           OR json_extract(p.data, '$.text') LIKE '%approach%'
           OR json_extract(p.data, '$.text') LIKE '%plan%'
           OR json_extract(p.data, '$.text') LIKE '%summary%')
    ORDER BY m.time_created ASC
    LIMIT 10
""", (sid,))
for r in cur.fetchall():
    print(f"ASSISTANT [{r[0]}]: {r[1][:400]}")
    print()

conn.close()
