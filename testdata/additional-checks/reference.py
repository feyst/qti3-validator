"""Writes expected.json: the findings of the ISO Schematron reference
implementation (the XSLT skeleton in lxml) for rules/qti3-additional-checks.sch
on every document in docs/. TestAdditionalChecksMatchReference compares the
validator's own engine with it. Run it after changing the rules or the
documents, from the repository root:

    docker run --rm -v "$PWD/rules:/rules:ro" -v "$PWD/testdata/additional-checks:/t" \
        python:3.12-alpine sh -c 'pip install -q lxml && python -I /t/reference.py'
"""
import json,glob,os
from lxml import etree, isoschematron
sch=isoschematron.Schematron(etree.parse('/rules/qti3-additional-checks.sch'),store_report=True)
SVRL='{http://purl.oclc.org/dsdl/svrl}'
out={}
for f in sorted(glob.glob('/t/docs/*.xml')):
    sch.validate(etree.parse(f))
    msgs=[]
    for e in sch.validation_report.iter(SVRL+'failed-assert'):
        m=' '.join(e.findtext(SVRL+'text').split())
        msgs.append(('[warning] ' if e.get('role')=='warning' else '')+m)
    out[os.path.basename(f)]=sorted(msgs)
json.dump(out,open('/t/expected.json','w'),indent=1,sort_keys=True,ensure_ascii=False)
