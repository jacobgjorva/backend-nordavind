#!/usr/bin/env python3
"""Mjødhallen AS: fiktiv norrøn drikkevaredistributør som speiler den reelle
kundeprofilen (B2B, ordrelinjer, sesong, utsnitt med årsfilter) — og som med
VILJE inneholder hver felleklasse vi har målt i prod:

  - nesten like kundenavn (fuzzy-fella: «Racamca» mot «Racamaca»)
  - spesialtegn i navn: æøå, &, /, é, ü, AS/AB-suffikser
  - konstant kolonne (kredittgrense = 0 overalt)
  - NULL-tung kolonne (kontakt_epost ~40 % tom)
  - sesongskjevhet + uferdig inneværende måned
  - join-fanout (3-6 linjer per ordre)
  - konsentrasjon (én hvalkunde tar ~1/3 av omsetningen)
  - død tabell (kampanjer, siste rad 2024)
  - databaseview v_salg + utsnitt v_salg_query med årsfilter (prod-mønsteret
    der utsnittet ERSTATTER basen i tillatelseslista)

Deterministisk (seed 42): samme fil hver gang, diffbar. Skriver ren SQL til
stdout — last med:  python3 scripts/mjodhallen_seed.py | psql "$DSN"
"""
import random
import sys
from datetime import date, timedelta

rng = random.Random(42)
TODAY = date(2026, 7, 27)

# --- kunder -----------------------------------------------------------------

FORLEDD = ["Valhall", "Yggdrasil", "Sleipner", "Gjallarhorn", "Mimisbrunnr",
           "Fenris", "Huginn", "Muninn", "Bifrost", "Ragnarok", "Einherjer",
           "Norne", "Vidar", "Balder", "Idunn", "Skadi", "Njord", "Heimdall",
           "Jotun", "Alvheim", "Midgard", "Utgard", "Frigg", "Tyr", "Brage",
           "Saga", "Embla", "Aegir", "Ran", "Urd", "Verdandi", "Skuld"]
ETTERLEDD = ["Pub", "Bar", "Kro", "Vinbar", "Gastropub", "Brasserie", "Kafé",
             "Bryggeri", "Taverna", "Skjenkestue", "Mjødstue", "Hall",
             "Restaurant", "Bistro", "Kjeller"]
BYER = [("Oslo", "0150", "Øst"), ("Bergen", "5003", "Vest"),
        ("Trondheim", "7010", "Midt"), ("Stavanger", "4006", "Vest"),
        ("Tromsø", "9008", "Nord"), ("Kristiansand", "4611", "Sør"),
        ("Ålesund", "6002", "Vest"), ("Bodø", "8006", "Nord"),
        ("Drammen", "3017", "Øst"), ("Fredrikstad", "1607", "Øst")]
SEKTORER = ["Restaurant", "Bar", "Hotell", "Festival", "Grossist", ""]

def sql_str(s):
    return "'" + s.replace("'", "''") + "'"

kunder = []
def add_kunde(navn, sektor=None, epost=None):
    by, postnr, region = rng.choice(BYER)
    kunder.append({
        "id": len(kunder) + 1,
        "navn": navn,
        # ~7 % mangler orgnr — ekte baser har hull.
        "orgnr": "" if rng.random() < 0.07 else str(rng.randint(910_000_000, 999_999_999)),
        "by": by, "postnr": postnr, "region": region,
        "sektor": sektor if sektor is not None else rng.choice(SEKTORER),
        # NULL-fella: ~40 % mangler kontakt.
        "epost": epost if epost is not None else (
            "" if rng.random() < 0.4 else f"post@{navn.split()[0].lower()}{len(kunder)}.no"),
        "sperret": rng.random() < 0.04,
        "opprettet": date(2019, 1, 1) + timedelta(days=rng.randint(0, 2400)),
    })

# Hvalen (konsentrasjons-fella) og kjedene.
add_kunde("Valhall Arena Servering AS", sektor="Festival", epost="innkjop@valhallarena.no")
for omr in ["Sentrum", "Aker Brygge", "Grünerløkka", "Bryggen", "Solsiden"]:
    add_kunde(f"Mjødhuset {omr} AS", sektor="Bar")

# Fuzzy-fellene: par som ligner, med den ene stavemåten i basen.
add_kunde("Ravnkroa Pub & Scene AS")
add_kunde("Ravnekroa Café & Scene AB")
add_kunde("Skjoldmøyene Taverna / Bergen")
add_kunde("Skjoldmøya Taverna AS")
add_kunde("Café Yggdrasil & Venner")
add_kunde("Brasserie Sleipner & Co AS")
add_kunde("Gjallarhorn Gastropub / Oslo")
add_kunde("Müller & Sønn Vinkjeller AS")
add_kunde("Mímisbrunnr Vinbar")

while len(kunder) < 1200:
    navn = f"{rng.choice(FORLEDD)} {rng.choice(ETTERLEDD)}"
    if rng.random() < 0.25:
        navn += f" / {rng.choice(BYER)[0]}"
    navn += rng.choice([" AS", " AS", " AB", ""])
    if any(k["navn"] == navn for k in kunder[-50:]):
        continue
    add_kunde(navn)

# --- produkter --------------------------------------------------------------

PRODUKT_MALER = [
    ("Odins Vrede {}", "mjød"), ("Frøyas Kyss {}", "mjød"),
    ("Muninn Mørk Mjød {}", "mjød"), ("Gullveig Honningbrygg {}", "mjød"),
    ("Einherjer Eplecider {}", "sider"), ("Idunns Eple {}", "sider"),
    ("Ragnarok Imperial Stout {}", "øl"), ("Bifrost Pale Ale {}", "øl"),
    ("Fenrisulv IPA {}", "øl"), ("Sleipners Åttende {}", "øl"),
    ("Heimdalls Vakt {}", "alkoholfritt"), ("Yggdrasil Urtebrygg {}", "alkoholfritt"),
]
produkter = []
for i in range(180):
    mal, kat = PRODUKT_MALER[i % len(PRODUKT_MALER)]
    arg = rng.choice(["2022", "2023", "2024", "2025", "Reserve", "Vinter", "Sommer", ""]).strip()
    produkter.append({
        "id": i + 1,
        "navn": (mal.format(arg)).strip(),
        "kategori": kat,
        "volum_cl": rng.choice([33, 50, 75]),
        "pris": round(rng.uniform(45, 420), 2),
        "aktiv": rng.random() > 0.15,
        "produsent": rng.choice(["Mjødhallen Eget Brygg", "Jotunheim Bryggeri AS",
                                 "Åsgard Sider & Co", "Nornebrygg AB"]),
    })

# --- ordrer med sesong og konsentrasjon ------------------------------------

def maanedsvekt(d):
    v = {12: 2.2, 7: 1.6, 6: 1.3, 11: 1.2, 1: 0.6, 2: 0.7}.get(d.month, 1.0)
    aar = {2022: 0.7, 2023: 0.9, 2024: 1.1, 2025: 1.2, 2026: 1.05}[d.year]
    return v * aar

# Kundevekter: hvalen dominerer, kjedene er store, resten zipf-aktig.
vekter = []
for k in kunder:
    if k["id"] == 1:
        vekter.append(600.0)
    elif k["navn"].startswith("Mjødhuset"):
        vekter.append(45.0)
    else:
        vekter.append(8.0 / (1 + (k["id"] % 97)))

ordrer, linjer = [], []
d = date(2022, 1, 1)
while d <= TODAY:
    antall_denne_dagen = max(0, int(rng.gauss(22 * maanedsvekt(d), 6)))
    for _ in range(antall_denne_dagen):
        kunde = rng.choices(kunder, weights=vekter)[0]
        oid = len(ordrer) + 1
        status = rng.choices(["levert", "venter", "kansellert", "utkast"],
                             weights=[88, 6, 4, 2])[0]
        ordrer.append({
            "id": oid, "kunde_id": kunde["id"], "dato": d, "status": status,
            "levering": d + timedelta(days=rng.randint(1, 9)) if status == "levert" else None,
        })
        for _ in range(rng.randint(3, 6)):
            p = rng.choice(produkter)
            n = rng.randint(6, 120)
            linjer.append({
                "id": len(linjer) + 1, "ordre_id": oid, "produkt_id": p["id"],
                "antall": n, "enhetspris": p["pris"],
                "linjesum": round(n * p["pris"], 2),
            })
    d += timedelta(days=1)

# --- SQL --------------------------------------------------------------------

w = sys.stdout.write
w("""-- Generert av scripts/mjodhallen_seed.py (seed 42). Ikke rediger for hånd.
BEGIN;
DROP VIEW IF EXISTS v_salg;
DROP TABLE IF EXISTS ordrelinjer, ordrer, produkter, kunder, kampanjer CASCADE;

CREATE TABLE kunder (
  id INT PRIMARY KEY, kundenavn TEXT NOT NULL, orgnr TEXT,
  postnr TEXT, by TEXT, region TEXT, sektor TEXT, kontakt_epost TEXT,
  kredittgrense NUMERIC NOT NULL DEFAULT 0, -- konstant-fella: 0 for alle
  sperret BOOLEAN NOT NULL, opprettet DATE NOT NULL
);
CREATE TABLE produkter (
  id INT PRIMARY KEY, produktnavn TEXT NOT NULL, kategori TEXT NOT NULL,
  volum_cl INT NOT NULL, pris NUMERIC NOT NULL, aktiv BOOLEAN NOT NULL,
  produsent TEXT NOT NULL
);
CREATE TABLE ordrer (
  id INT PRIMARY KEY, kunde_id INT NOT NULL REFERENCES kunder(id),
  ordredato DATE NOT NULL, status TEXT NOT NULL, leveringsdato DATE
);
CREATE TABLE ordrelinjer (
  id INT PRIMARY KEY, ordre_id INT NOT NULL REFERENCES ordrer(id),
  produkt_id INT NOT NULL REFERENCES produkter(id),
  antall INT NOT NULL, enhetspris NUMERIC NOT NULL, linjesum NUMERIC NOT NULL
);
CREATE TABLE kampanjer ( -- død tabell: ingenting etter 2024
  id INT PRIMARY KEY, navn TEXT NOT NULL,
  start_dato DATE NOT NULL, slutt_dato DATE NOT NULL, rabatt_pst NUMERIC NOT NULL
);
""")

w("COPY kunder FROM stdin;\n")
for k in kunder:
    epost = k["epost"] if k["epost"] else "\\N"
    orgnr = k["orgnr"] if k["orgnr"] else "\\N"
    sektor = k["sektor"] if k["sektor"] else "\\N"
    w(f"{k['id']}\t{k['navn']}\t{orgnr}\t{k['postnr']}\t{k['by']}\t{k['region']}\t"
      f"{sektor}\t{epost}\t0\t{k['sperret']}\t{k['opprettet']}\n")
w("\\.\n")

w("COPY produkter FROM stdin;\n")
for p in produkter:
    w(f"{p['id']}\t{p['navn']}\t{p['kategori']}\t{p['volum_cl']}\t{p['pris']}\t{p['aktiv']}\t{p['produsent']}\n")
w("\\.\n")

w("COPY ordrer FROM stdin;\n")
for o in ordrer:
    lev = o["levering"] if o["levering"] else "\\N"
    w(f"{o['id']}\t{o['kunde_id']}\t{o['dato']}\t{o['status']}\t{lev}\n")
w("\\.\n")

w("COPY ordrelinjer FROM stdin;\n")
for l in linjer:
    w(f"{l['id']}\t{l['ordre_id']}\t{l['produkt_id']}\t{l['antall']}\t{l['enhetspris']}\t{l['linjesum']}\n")
w("\\.\n")

w("""COPY kampanjer FROM stdin;
1\tJulemjød 2022\t2022-11-15\t2022-12-31\t12
2\tSommersider 2023\t2023-06-01\t2023-08-15\t8
3\tRagnarok-lansering\t2024-02-01\t2024-03-01\t15
4\tJulemjød 2024\t2024-11-10\t2024-12-31\t12
\\.

-- Databaseview: hele salgshistorikken flatet ut (join-fanout er reell).
CREATE VIEW v_salg AS
SELECT o.id AS ordre_id, o.ordredato, o.status,
       k.kundenavn, k.region, p.produktnavn, p.kategori,
       l.antall, l.linjesum AS netto_omsetning
FROM ordrelinjer l
JOIN ordrer o ON l.ordre_id = o.id
JOIN kunder k ON o.kunde_id = k.id
JOIN produkter p ON l.produkt_id = p.id;

CREATE INDEX idx_ordrer_dato ON ordrer(ordredato);
CREATE INDEX idx_ordrer_kunde ON ordrer(kunde_id);
CREATE INDEX idx_linjer_ordre ON ordrelinjer(ordre_id);
COMMIT;
ANALYZE;
""")

print(f"-- kunder={len(kunder)} produkter={len(produkter)} "
      f"ordrer={len(ordrer)} linjer={len(linjer)}", file=sys.stderr)
