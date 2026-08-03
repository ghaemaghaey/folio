import sqlite3, json

conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()

# Search for user messages containing rule/decision keywords
keywords = ["always", "never", "remember", "rule", "decision", "decided", "tradeoff", "reason", "workflow", "repeat"]

for kw in keywords:
    cur.execute("""
        SELECT m.id, m.session_id, substr(json_extract(p.data, '$.text'), 1, 300) as text_preview
        FROM message m
        JOIN part p ON p.message_id = m.id
        WHERE json_extract(m.data, '$.role') = 'user'
          AND json_extract(p.data, '$.type') = 'text'
          AND json_extract(p.data, '$.text') LIKE ?
        ORDER BY m.time_created DESC
        LIMIT 3
    """, (f'%{kw}%',))
    rows = cur.fetchall()
    if rows:
        print(f"\n=== Keyword: {kw} ===")
        for r in rows:
            print(f"  [{r[1]}] {r[2][:200]}")

conn.close()
