# Generates random QTI-like documents that exercise the Schematron rules.
# Usage: python3 generate.py <imsqti_asiv3p0_v1p0.xsd> <out-dir> <count>
import os, random, re, sys
src, out, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
xsd = open(src, encoding='utf-8').read()
rules = ' '.join(re.findall(r'<sch:rule[^>]*>.*?</sch:rule>', xsd, re.S))
names = sorted(set(re.findall(r'qti:(qti-[a-z-]+)', rules)) | {'qti-item-body', 'qti-assessment-item', 'p', 'h1', 'h2', 'div', 'span', 'img', 'source', 'table'})
attr_names = sorted(set(re.findall(r"string\(name\(@\*\[\d+\]\)\)='([^']+)'", xsd)))
numeric = ['max-choices', 'min-choices', 'match-max', 'match-min', 'max-associations', 'min-associations',
           'max-plays', 'min-plays', 'upper-bound', 'lower-bound', 'mastery-value', 'normal-minimum', 'normal-maximum']
ids = ['RESPONSE', 'R2', 'SCORE', 'X']
rnd = random.Random(42)
def attrs():
    a = {}
    for _ in range(rnd.randint(0, 5)):
        k = rnd.random()
        if k < 0.35:
            a[rnd.choice(attr_names)] = rnd.choice(ids + ['1', 'true', 'x'])
        elif k < 0.6:
            a[rnd.choice(numeric)] = str(rnd.randint(0, 3))
        elif k < 0.7:
            a['role'] = rnd.choice(['heading', 'tab', 'presentation', 'button', 'list'])
        elif k < 0.8:
            a[rnd.choice(['response-identifier', 'string-identifier', 'identifier', 'templateIdentifier'])] = rnd.choice(ids)
        elif k < 0.9:
            a[rnd.choice(['base-type', 'cardinality'])] = rnd.choice(['string', 'float', 'integer', 'file', 'directedPair', 'boolean', 'single', 'multiple'])
        else:
            a[rnd.choice(['colour', 'zzz', 'data-x', 'xml:lang'])] = 'v'
    return ''.join(f' {k}="{v}"' for k, v in a.items())
def elem(depth):
    name = rnd.choice(names)
    kids = ''.join(elem(depth + 1) for _ in range(rnd.randint(0, 3 if depth < 4 else 0)))
    text = rnd.choice(['', 'x', ' 2 '])
    return f'<{name}{attrs()}>{text}{kids}</{name}>'
for i in range(n):
    decls = ''.join(f'<qti-response-declaration identifier="{rnd.choice(ids)}" cardinality="{rnd.choice(["single","multiple"])}" base-type="{rnd.choice(["string","identifier","float","file"])}"/>' for _ in range(rnd.randint(0, 3)))
    body = ''.join(elem(1) for _ in range(rnd.randint(1, 4)))
    doc = f'<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"{attrs()}>{decls}<qti-item-body>{body}</qti-item-body></qti-assessment-item>'
    open(os.path.join(out, f'fuzz-{i:04d}.xml'), 'w', encoding='utf-8').write(doc)
print(len(names), 'element names,', len(attr_names), 'attribute names')
