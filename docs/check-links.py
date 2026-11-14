#!/usr/bin/env python3
# check-links.py <built site>: every link from one page of the site to another, and its #anchor, exists. A link to
# an alias (an old URL that only redirects) is broken too: it should name the page itself.
import os
import sys
from html.parser import HTMLParser
from urllib.parse import unquote, urlsplit

ROOT, HOST = sys.argv[1], "natkit.ulm0.com"


class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids, self.links, self.alias = set(), [], False

    def handle_starttag(self, tag, attrs):
        a = dict(attrs)
        if a.get("id"):
            self.ids.add(a["id"])
        if tag == "a" and a.get("href"):
            self.links.append(a["href"])
        if tag == "meta" and (a.get("http-equiv") or "").lower() == "refresh":
            self.alias = True


pages = {}  # URL path -> Page
for d, _, files in os.walk(ROOT):
    for f in files:
        if f.endswith(".html"):
            p = Page()
            with open(os.path.join(d, f), encoding="utf-8") as fh:
                p.feed(fh.read())
            path = "/" + os.path.relpath(os.path.join(d, f), ROOT).replace(os.sep, "/")
            pages[path.removesuffix("index.html")] = p

bad = []
for src, page in pages.items():
    if page.alias:
        continue
    for href in page.links:
        u = urlsplit(href)
        if (u.scheme and u.scheme not in ("http", "https")) or (u.netloc and u.netloc != HOST):
            continue
        path = unquote(u.path) or src
        if not path.startswith("/"):
            path = os.path.normpath(os.path.join(src, path)) + ("/" if path.endswith("/") else "")
        if path in pages:
            dst = pages[path]
        elif path + "/" in pages:
            dst = pages[path + "/"]
        elif os.path.isfile(os.path.join(ROOT, path.lstrip("/"))):
            continue  # an image, a feed, a file
        else:
            bad.append(f"{src}: {href}: no such page")
            continue
        if dst.alias:
            bad.append(f"{src}: {href}: an alias, link the page itself")
        elif u.fragment and u.fragment not in dst.ids:
            bad.append(f"{src}: {href}: no #{u.fragment} on that page")

print("\n".join(sorted(set(bad))) or f"{len(pages)} pages, every link resolves")
sys.exit(1 if bad else 0)
