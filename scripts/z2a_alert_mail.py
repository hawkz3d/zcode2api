#!/usr/bin/env python3
"""z2a_alert_mail.py — ZCode 抢包告警邮件（复用 Uptime Kuma 已配置的 SMTP 通道）。

用法: z2a_alert_mail.py <subject> <body>
设计: SMTP 参数每次运行时从 Kuma 的 kuma.db 读取——密码不复制、不落盘到本插件任何文件。
      Kuma 容器数据卷宿主路径 /opt/uptime-kuma/data/kuma.db（other 位可读）。
"""
import sqlite3
import smtplib
import ssl
import sys
from email.mime.text import MIMEText
from email.header import Header
from email.utils import formataddr

KUMA_DB = "/opt/uptime-kuma/data/kuma.db"


def smtp_conf():
    con = sqlite3.connect(f"file:{KUMA_DB}?mode=ro", uri=True)
    try:
        cur = con.cursor()
        cols = [c[1] for c in cur.execute("PRAGMA table_info(notification)")]
        for row in cur.execute("SELECT * FROM notification WHERE active=1"):
            d = dict(zip(cols, row))
            cfg = _cfg(d)
            if cfg.get("type") == "smtp":
                return cfg
    finally:
        con.close()
    return None


def _cfg(d):
    import json
    raw = d.get("config")
    if isinstance(raw, str) and raw.strip():
        try:
            return json.loads(raw)
        except ValueError:
            return {}
    return raw if isinstance(raw, dict) else {}


def send(subject, body):
    cfg = smtp_conf()
    if not cfg:
        print("no active smtp notification in kuma.db", file=sys.stderr)
        return 2
    host = cfg.get("smtpHost")
    port = int(cfg.get("smtpPort") or (465 if cfg.get("smtpSecure") in (True, "true", "ssl") else 587))
    user = cfg.get("smtpUsername") or None
    pwd = cfg.get("smtpPassword") or None
    frm = cfg.get("smtpFrom") or user
    to = cfg.get("smtpTo")
    if not host or not to:
        print("smtp host/to missing", file=sys.stderr)
        return 2

    msg = MIMEText(body, "plain", "utf-8")
    msg["Subject"] = Header(subject, "utf-8")
    msg["From"] = formataddr((str(Header("ZCode抢包哨兵", "utf-8")), frm or user or host))
    msg["To"] = to

    if port == 465:
        srv = smtplib.SMTP_SSL(host, port, timeout=30,
                               context=ssl.create_default_context())
    else:
        srv = smtplib.SMTP(host, port, timeout=30)
    try:
        srv.ehlo()
        if port != 465:
            try:
                srv.starttls(context=ssl.create_default_context())
                srv.ehlo()
            except smtplib.SMTPNotSupportedError:
                pass  # 明文端口按服务器能力走
        if user and pwd:
            srv.login(user, pwd)
        srv.sendmail(frm or user, [to], msg.as_string())
    finally:
        try:
            srv.quit()
        except Exception:
            pass
    return 0


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("usage: z2a_alert_mail.py <subject> <body>", file=sys.stderr)
        sys.exit(1)
    sys.exit(send(sys.argv[1], sys.argv[2]))
