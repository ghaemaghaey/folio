import sqlite3, json

conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()

# Get folio project sessions (non-checkpoint-writer)
folio_project = "218818d7-455a-453a-bf2c-a29cf7ffcb7d"

cur.execute("""
    SELECT s.id, s.title, datetime(s.time_created, 'unixepoch') as created
    FROM session s
    WHERE s.project_id = ?
      AND s.title NOT LIKE 'checkpoint-writer:%'
    ORDER BY s.time_created DESC
    LIMIT 20
""", (folio_project,))
sessions = cur.fetchall()

for sid, title, created in sessions:
    # Get first few user messages from this session
    cur.execute("""
        SELECT substr(json_extract(p.data, '$.text'), 1, 500) as text_preview
        FROM message m
        JOIN part p ON p.message_id = m.id
        WHERE m.session_id = ?
          AND json_extract(m.data, '$.role') = 'user'
          AND json_extract(p.data, '$.type') = 'text'
          AND json_extract(p.data, '$.text') NOT LIKE '%system-reminder%'
        ORDER BY m.time_created ASC
        LIMIT 3
    """, (sid,))
    user_msgs = cur.fetchall()
    if user_msgs:
        print(f"\n=== Session: {title[:60]} ({sid}) ===")
        for (txt,) in user_msgs:
            print(f"  USER: {txt[:300]}")

conn.close()
