import json,glob,os,re,sys
# Token-dekning, samme prinsipp som prods grounding.go: et navn er dekket
# når HVERT innholdsord finnes i kildene. Frasematch gir falske alarmer
# fordi modellen lovlig setter sammen produktnavn + leverandør fra URL.
def norm(s): return re.sub(r'[^a-zà-ÿ0-9]+',' ', s.lower())
STOP={'fra','og','i','per','for','som','av','de','den','det','en','et','er','til','år','kroner','andre','aktører'}
base=sys.argv[1]
files={}
for dd in sorted(glob.glob(base+'/*')):
    for f in glob.glob(dd+'/*.json'):
        files[os.path.basename(f)[:-5]]=f
for cid in sorted(files):
    t=json.load(open(files[cid]))
    src=norm('\n'.join(e['result'] for e in (t.get('evidence') or [])))
    srcdig=re.sub(r'\D','',src)
    ans=t['answer']
    print(f"=== {cid}  ({len(t.get('evidence') or [])} verktøykall)")
    bad=0
    for b in set(re.findall(r'\*\*([^*]{2,45})\*\*', ans)):
        toks=[w for w in norm(b).split() if w not in STOP and len(w)>2 and not w.isdigit()]
        if not toks: continue
        missing=[w for w in toks if w not in src]
        if missing:
            print("   *** UDEKKET NAVN", repr(b), "mangler:", missing); bad+=1
    for n in set(re.findall(r'\d[\d\s.,]{2,}\d', ans)):
        d=re.sub(r'\D','',n)
        if len(d)>=3 and d not in srcdig:
            print("   *** UDEKKET TALL", repr(n.strip())); bad+=1
    if not bad: print("   alt dekket av kildene")
