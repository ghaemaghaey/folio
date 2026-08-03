import sqlite3, json

conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()

# Search for actual user messages (not system-reminder, not checkpoint-writer sessions)
# that contain durable-signal keywords
keywords = ["always", "never", "remember", "rule", "decision", "decided", "preference"]

for kw in keywords:
    cur.execute("""
        SELECT m.id, m.session_id, substr(json_extract(p.data, '$.text'), 1, 400) as text_preview
        FROM message m
        JOIN part p ON p.message_id = m.id
        JOIN session s ON s.id = m.session_id
        WHERE json_extract(m.data, '$.role') = 'user'
          AND json_extract(p.data, '$.type') = 'text'
          AND s.title NOT LIKE 'checkpoint-writer:%'
          AND json_extract(p.data, '$.text') NOT LIKE '%system-reminder%'
          AND json_extract(p.data, '$.text') LIKE ?
        ORDER BY m.time_created DESC
        LIMIT 5
    """, (f'%{kw}%',))
    rows = cur.fetchall()
    if rows:
        print(f"\n=== Keyword: {kw} ===")
        for r in rows:
            print(f"  [{r[1]}] {r[2][:300]}")

conn.close()
