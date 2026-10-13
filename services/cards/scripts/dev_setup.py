"""Additive migration, fixed loopback dev database, no production defaults."""
import json,sys
from pathlib import Path
ROOT=Path(__file__).resolve().parents[3]
sys.path.insert(0,str(ROOT/'dev/scripts'))
from bootstrap import settings,mysql_connection

def main():
 v=settings();conn=mysql_connection(v)
 try:
  cur=conn.cursor();cur.execute('SELECT DATABASE()');assert cur.fetchone()[0]=='cpm_dev'
  for statement in (ROOT/'services/cards/migrations/001_cards.sql').read_text().split(';'):
   if statement.strip():cur.execute(statement)
  conn.commit()
 finally:conn.close()
 p=ROOT/'dev/local/cards.env';env={k:v[k] for k in ['MYSQL_USER','MYSQL_PASSWORD','AUTH_INTERNAL_KEY']};env.update(MYSQL_HOST='127.0.0.1',MYSQL_PORT='13306',MYSQL_DATABASE='cpm_dev',CARDS_LISTEN_ADDR='127.0.0.1:8008',CARDS_AUTH_URL='http://127.0.0.1:8002',CARDS_CORE_URL='http://127.0.0.1:8003',CARDS_TESTS_URL='http://127.0.0.1:8004')
 p.write_text('\n'.join(f'{k}={v}' for k,v in env.items())+'\n');p.chmod(0o600)
 print('Cards additive schema and private local configuration ready.')
if __name__=='__main__':main()
