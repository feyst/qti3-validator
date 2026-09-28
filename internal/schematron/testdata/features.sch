<?xml version="1.0" encoding="UTF-8"?>
<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron" queryBinding="xslt">
  <sch:title>Feature coverage</sch:title>
  <sch:ns prefix="o" uri="urn:order"/>
  <sch:let name="maxQty" value="10"/>

  <sch:phase id="only-basic"><sch:active pattern="basic"/></sch:phase>

  <sch:pattern id="basic">
    <sch:let name="ids" value="//o:line/@id"/>
    <sch:rule context="o:order">
      <sch:assert test="o:line" id="has-lines">An order has at least one line.</sch:assert>
      <sch:report test="count(o:line) &gt; 3" role="warning">Large order: <sch:value-of select="count(o:line)"/> lines.</sch:report>
    </sch:rule>
    <!-- First match wins: lines with qty are checked here, not by the next rule. -->
    <sch:rule context="o:line[@qty]">
      <sch:let name="qty" value="number(@qty)"/>
      <sch:assert test="$qty &lt;= $maxQty" diagnostics="qty-diag">Quantity <sch:value-of select="$qty"/> of <sch:name/> exceeds <sch:emph>max</sch:emph> <sch:value-of select="$maxQty"/>.</sch:assert>
    </sch:rule>
    <sch:rule context="o:line">
      <sch:assert test="false()" id="no-qty">Line <sch:value-of select="@id"/> has no qty.</sch:assert>
    </sch:rule>
    <sch:rule context="o:ref">
      <sch:assert test="@to = $ids">Reference <sch:value-of select="@to"/> points to no line.</sch:assert>
      <sch:assert test="count(//o:line[@id = current()/@to]) = 1">Reference must match exactly one line.</sch:assert>
    </sch:rule>
  </sch:pattern>

  <sch:pattern id="attributes">
    <sch:rule context="@code | o:note/@lang">
      <sch:assert test="string-length(.) = 3">Attribute <sch:name/> must have 3 characters, has '<sch:value-of select="."/>'.</sch:assert>
    </sch:rule>
  </sch:pattern>

  <sch:pattern abstract="true" id="required-child">
    <sch:rule context="$parent">
      <sch:assert test="$child">Element <sch:name/> needs <sch:value-of select="'$child'"/>.</sch:assert>
    </sch:rule>
  </sch:pattern>
  <sch:pattern is-a="required-child" id="order-has-customer">
    <sch:param name="parent" value="o:order"/>
    <sch:param name="child" value="o:customer"/>
  </sch:pattern>

  <sch:pattern id="extends">
    <sch:rule abstract="true" id="named">
      <sch:assert test="normalize-space(@name) != ''">A <sch:name/> needs a name.</sch:assert>
    </sch:rule>
    <sch:rule context="o:customer">
      <sch:extends rule="named"/>
      <sch:assert test="@vip = 'true' or @vip = 'false' or not(@vip)">vip must be a boolean.</sch:assert>
    </sch:rule>
  </sch:pattern>

  <sch:diagnostics>
    <sch:diagnostic id="qty-diag">Lower the quantity of line <sch:value-of select="@id"/>.</sch:diagnostic>
  </sch:diagnostics>
</sch:schema>
