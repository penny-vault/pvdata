#!/usr/bin/env python3
"""
One-time script to scrape Key Facts from iShares product pages and update
ishares_etfs.json with benchmarkIndex, bloombergTicker, and active flag.

Uses 1-2 second random delay between requests.
"""

import json
import re
import time
import random
import urllib.request

INPUT_FILE = "provider/ishares_etfs.json"
OUTPUT_FILE = "provider/ishares_etfs.json"

HEADERS = {
    "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
                  "AppleWebKit/537.36 (KHTML, like Gecko) "
                  "Chrome/120.0.0.0 Safari/537.36",
    "Accept": "text/html,*/*",
}

# col-indexSeriesName -> Benchmark Index
BENCHMARK_RE = re.compile(
    r'col-indexSeriesName.*?<div class="data">\s*([^<]+)',
    re.DOTALL,
)

# col-indexTicker -> Bloomberg Index Ticker
BLOOMBERG_RE = re.compile(
    r'col-indexTicker.*?<div class="data">\s*([^<]+)',
    re.DOTALL,
)

# Active ETF marker
ACTIVE_RE = re.compile(r'data-sectionCode="product-page\.footer\.disclaimer:active-etf-cav')


def fetch_key_facts(product_id, slug):
    """Fetch key facts for a single iShares ETF."""
    url = f"https://www.ishares.com/us/products/{product_id}/{slug}"
    req = urllib.request.Request(url, headers=HEADERS)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            html = resp.read().decode("utf-8", errors="replace")
    except Exception as e:
        return None, str(e)

    result = {}

    m = BENCHMARK_RE.search(html)
    if m:
        result["benchmarkIndex"] = m.group(1).strip()

    m = BLOOMBERG_RE.search(html)
    if m:
        result["bloombergTicker"] = m.group(1).strip()

    result["active"] = bool(ACTIVE_RE.search(html))

    return result, None


def main():
    with open(INPUT_FILE) as f:
        etfs = json.load(f)

    total = len(etfs)
    failed = []
    active_count = 0

    for i, etf in enumerate(etfs):
        ticker = etf["ticker"]
        product_id = etf["productId"]
        slug = etf["slug"]

        print(f"[{i+1}/{total}] {ticker}: fetching...", end=" ", flush=True)

        facts, err = fetch_key_facts(product_id, slug)
        if err:
            print(f"FAILED: {err}")
            failed.append(ticker)
        else:
            parts = []
            if facts.get("benchmarkIndex"):
                etf["benchmarkIndex"] = facts["benchmarkIndex"]
                parts.append(f"benchmark={facts['benchmarkIndex']}")
            if facts.get("bloombergTicker"):
                etf["bloombergTicker"] = facts["bloombergTicker"]
                parts.append(f"bbg={facts['bloombergTicker']}")
            if facts.get("active"):
                etf["active"] = True
                active_count += 1
                parts.append("ACTIVE")
            else:
                etf["active"] = False

            print(", ".join(parts) if parts else "no key facts found")

        # Random delay 1-2 seconds between requests
        if i < total - 1:
            delay = 1 + random.random()
            time.sleep(delay)

    # Write updated JSON
    with open(OUTPUT_FILE, "w") as f:
        json.dump(etfs, f, indent=2)
        f.write("\n")

    print(f"\nDone. Updated {total - len(failed)}/{total} ETFs.")
    print(f"Active ETFs: {active_count}")
    if failed:
        print(f"Failed: {', '.join(failed)}")


if __name__ == "__main__":
    main()
