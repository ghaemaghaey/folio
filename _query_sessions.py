import sqlite3
conn = sqlite3.connect(r'C:\Users\ghaem\.local\share\mimocode\mimocode.db')
cur = conn.cursor()
# Get all sessions excluding checkpoint-writer ones
cur.execute("SELECT id, title, datetime(time_created, 'unixepoch') as created FROM session WHERE title NOT LIKE 'checkpoint-writer:%' ORDER BY time_created DESC LIMIT 30")
for r in cur.fetchall():
    title = (r[1][:80] if r[1] else "(no title)")
    print(f"{r[0]}  {title}  {r[2]}")
conn.close()
