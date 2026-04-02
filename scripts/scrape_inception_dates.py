#!/usr/bin/env python3
"""
One-time script to scrape inception dates from iShares product pages
and update ishares_etfs.json with inceptionDate fields.

Uses curl-style requests with a 3-5 second random delay between requests
to avoid hammering the service.
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

# Regex to extract inception date from the product page HTML:
#   <div class="product-data-item col-launchDate ">
#     <div class="caption" ...>Fund Inception...</div>
#     <div class="data">May 15, 2000</div>
INCEPTION_RE = re.compile(
    r'col-launchDate.*?<div class="data">\s*([A-Z][a-z]+ \d{1,2}, \d{4})',
    re.DOTALL,
)

# Month name -> number
MONTHS = {
    "Jan": "01", "Feb": "02", "Mar": "03", "Apr": "04",
    "May": "05", "Jun": "06", "Jul": "07", "Aug": "08",
    "Sep": "09", "Oct": "10", "Nov": "11", "Dec": "12",
}

DATE_RE = re.compile(r"([A-Z][a-z]+) (\d{1,2}), (\d{4})")


def parse_date_to_iso(date_str):
    """Convert 'May 15, 2000' to '2000-05-15'."""
    m = DATE_RE.match(date_str)
    if not m:
        return None
    month_name, day, year = m.group(1), m.group(2), m.group(3)
    month_num = MONTHS.get(month_name[:3])
    if not month_num:
        return None
    return f"{year}-{month_num}-{int(day):02d}"


def fetch_inception_date(product_id, slug):
    """Fetch the inception date for a single iShares ETF."""
    url = f"https://www.ishares.com/us/products/{product_id}/{slug}"
    req = urllib.request.Request(url, headers=HEADERS)
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            html = resp.read().decode("utf-8", errors="replace")
    except Exception as e:
        return None, str(e)

    match = INCEPTION_RE.search(html)
    if match:
        raw_date = match.group(1).strip()
        iso_date = parse_date_to_iso(raw_date)
        return iso_date, None
    return None, "inception date not found in HTML"


def main():
    with open(INPUT_FILE) as f:
        etfs = json.load(f)

    total = len(etfs)
    failed = []

    for i, etf in enumerate(etfs):
        ticker = etf["ticker"]
        product_id = etf["productId"]
        slug = etf["slug"]

        # Skip if already has inception date
        if etf.get("inceptionDate"):
            print(f"[{i+1}/{total}] {ticker}: already has {etf['inceptionDate']}, skipping")
            continue

        print(f"[{i+1}/{total}] {ticker}: fetching...", end=" ", flush=True)

        inception, err = fetch_inception_date(product_id, slug)
        if inception:
            etf["inceptionDate"] = inception
            print(f"{inception}")
        else:
            print(f"FAILED: {err}")
            failed.append(ticker)

        # Random delay 1-2 seconds between requests
        if i < total - 1:
            delay = 1 + random.random()
            time.sleep(delay)

    # Write updated JSON
    with open(OUTPUT_FILE, "w") as f:
        json.dump(etfs, f, indent=2)
        f.write("\n")

    print(f"\nDone. Updated {total - len(failed)}/{total} ETFs.")
    if failed:
        print(f"Failed: {', '.join(failed)}")


if __name__ == "__main__":
    main()
