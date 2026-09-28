<?xml version="1.0" encoding="UTF-8"?>
<!--
  Additional checks for QTI 3 items and tests.

  These rules run on every QTI 3.0.0 and 3.0.1 document, after the XSD and
  the Schematron rules 1EdTech embeds in the XSDs. Each rule checks either a
  requirement the QTI 3 specification states in so many words, or something
  that makes an item impossible to run as written. Style and quality are out
  of scope. docs/additional-checks.md describes every check and quotes its
  basis in the specification:
  https://www.imsglobal.org/spec/qti/v3p0/info

  The pattern id is what a report shows as the generator of a finding
  ("schematron|<pattern id>"), so it stays stable once released. A failed
  assert is an error; one with role="warning" is a warning.

  Only XPath 1.0 within the document is used. Checks that need other files
  in a package are not Schematron and live in the validator itself.

  The build compiles this file (go run ./cmd/fetchschemas); run that again
  after a change.
-->
<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron" queryBinding="xslt">
  <sch:title>QTI 3 additional checks</sch:title>
  <sch:ns prefix="qti" uri="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>

  <!-- The variables a document declares. -->
  <sch:let name="responses" value="//qti:qti-response-declaration/@identifier"/>
  <sch:let name="outcomes" value="//qti:qti-outcome-declaration/@identifier"/>
  <sch:let name="templates" value="//qti:qti-template-declaration/@identifier"/>
  <sch:let name="contexts" value="//qti:qti-context-declaration/@identifier"/>

  <!--
    Interactions must be bound to a declared response variable.
    "All variables must be declared"; response-identifier is "The response
    variable associated with the interaction".
  -->
  <sch:pattern id="response-declaration-exists">
    <sch:rule context="qti:qti-assessment-item//qti:*[@response-identifier]">
      <sch:assert test="@response-identifier = $responses">
        <sch:name/> is bound to '<sch:value-of select="@response-identifier"/>', which is not a declared response variable.
      </sch:assert>
      <sch:assert test="not(@string-identifier) or @string-identifier = $responses">
        <sch:name/> has string-identifier '<sch:value-of select="@string-identifier"/>', which is not a declared response variable.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    Expressions and displayed variables must refer to declared or built-in
    variables. "All variables must be declared except for the built-in
    session variables"; built in are numAttempts, duration (response),
    completionStatus (outcome) and QTI_CONTEXT (context).
  -->
  <sch:pattern id="variable-declared">
    <sch:rule context="qti:qti-assessment-item//qti:qti-correct | qti:qti-assessment-item//qti:qti-map-response | qti:qti-assessment-item//qti:qti-map-response-point">
      <sch:assert test="@identifier = $responses">
        <sch:name/> refers to '<sch:value-of select="@identifier"/>', which is not a declared response variable.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:qti-variable | qti:qti-assessment-item//qti:qti-default">
      <sch:assert test="@identifier = $responses or @identifier = $outcomes or @identifier = $templates or @identifier = $contexts
                        or @identifier = 'numAttempts' or @identifier = 'duration' or @identifier = 'completionStatus' or @identifier = 'QTI_CONTEXT'">
        <sch:name/> refers to '<sch:value-of select="@identifier"/>', which is not a declared variable.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:qti-printed-variable">
      <sch:assert test="@identifier = $outcomes or @identifier = $templates or @identifier = 'completionStatus'">
        <sch:name/> refers to '<sch:value-of select="@identifier"/>', which is not a declared outcome or template variable.
      </sch:assert>
    </sch:rule>
    <!--
      In a test, a name with a dot refers to a variable of an item, section
      or test part (checked by test-item-variable); any other name to a
      variable of the test.
    -->
    <sch:rule context="qti:qti-assessment-test//qti:qti-variable[not(contains(@identifier, '.'))] | qti:qti-assessment-test//qti:qti-default">
      <sch:assert test="@identifier = $outcomes or @identifier = $contexts or @identifier = 'duration' or @identifier = 'QTI_CONTEXT'">
        <sch:name/> refers to '<sch:value-of select="@identifier"/>', which is not a variable declared in the test.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    Rules that set a variable must name one of the right kind. "A references
    must match the identifier in a corresponding 'template declaration'";
    "All variables must be declared". Only in items and tests: a standalone
    response processing template has no declarations of its own.
  -->
  <sch:pattern id="set-target-declared">
    <sch:rule context="qti:qti-assessment-item//qti:qti-set-outcome-value | qti:qti-assessment-item//qti:qti-lookup-outcome-value
                       | qti:qti-assessment-test//qti:qti-set-outcome-value | qti:qti-assessment-test//qti:qti-lookup-outcome-value">
      <sch:assert test="@identifier = $outcomes or @identifier = 'completionStatus'">
        <sch:name/> sets '<sch:value-of select="@identifier"/>', which is not a declared outcome variable.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:qti-set-template-value">
      <sch:assert test="@identifier = $templates">
        <sch:name/> sets '<sch:value-of select="@identifier"/>', which is not a declared template variable.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:qti-set-correct-response">
      <sch:assert test="@identifier = $responses">
        <sch:name/> sets '<sch:value-of select="@identifier"/>', which is not a declared response variable.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:qti-set-default-value">
      <sch:assert test="@identifier = $responses or @identifier = $outcomes">
        <sch:name/> sets '<sch:value-of select="@identifier"/>', which is not a declared response or outcome variable.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    Feedback and conditional content are shown or hidden by an identifier
    variable: "The identifier of an outcome variable that must have a
    base-type of identifier and be of either single or multiple
    cardinality", and the same for template variables.
  -->
  <sch:pattern id="show-hide-variable">
    <sch:rule context="qti:qti-assessment-item//qti:*[@outcome-identifier] | qti:qti-assessment-test//qti:qti-test-feedback">
      <sch:let name="decl" value="//qti:qti-outcome-declaration[@identifier = current()/@outcome-identifier]"/>
      <sch:assert test="$decl or @outcome-identifier = 'completionStatus'">
        <sch:name/> refers to '<sch:value-of select="@outcome-identifier"/>', which is not a declared outcome variable.
      </sch:assert>
      <sch:assert test="not($decl) or ($decl/@base-type = 'identifier' and ($decl/@cardinality = 'single' or $decl/@cardinality = 'multiple'))">
        <sch:name/> refers to outcome variable '<sch:value-of select="@outcome-identifier"/>', which must have base-type identifier and single or multiple cardinality.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:*[@template-identifier][not(self::qti:qti-template-variable)]">
      <sch:let name="decl" value="//qti:qti-template-declaration[@identifier = current()/@template-identifier]"/>
      <sch:assert test="$decl">
        <sch:name/> refers to '<sch:value-of select="@template-identifier"/>', which is not a declared template variable.
      </sch:assert>
      <sch:assert test="not($decl) or ($decl/@base-type = 'identifier' and ($decl/@cardinality = 'single' or $decl/@cardinality = 'multiple'))">
        <sch:name/> refers to template variable '<sch:value-of select="@template-identifier"/>', which must have base-type identifier and single or multiple cardinality.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    "There are two built-in response variables: 'numAttempts' and
    'duration'. These are declared implicitly and must not appear in a
    'response declaration'." The same holds for the outcome variable
    'completionStatus'.
  -->
  <sch:pattern id="builtin-not-declared">
    <sch:rule context="qti:qti-response-declaration[@identifier = 'numAttempts' or @identifier = 'duration']">
      <sch:assert test="false()">
        The built-in response variable '<sch:value-of select="@identifier"/>' must not be declared.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-outcome-declaration[@identifier = 'completionStatus']">
      <sch:assert test="false()">
        The built-in outcome variable 'completionStatus' must not be declared.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    Identifiers must be unique. Variables are UniqueIdentifiers, "unique
    within the scope of the instance". A choice's identifier "must not be
    used by any other choice"; 1EdTech's own examples reuse choice
    identifiers in different interactions of one item, so this is checked
    within the interaction, where a duplicate makes a response ambiguous.
  -->
  <sch:pattern id="identifier-unique">
    <sch:rule context="qti:qti-assessment-item/qti:qti-response-declaration | qti:qti-assessment-item/qti:qti-outcome-declaration
                       | qti:qti-assessment-item/qti:qti-template-declaration | qti:qti-assessment-item/qti:qti-context-declaration
                       | qti:qti-assessment-test/qti:qti-outcome-declaration | qti:qti-assessment-test/qti:qti-context-declaration">
      <sch:assert test="count(../*[self::qti:qti-response-declaration or self::qti:qti-outcome-declaration or self::qti:qti-template-declaration or self::qti:qti-context-declaration][@identifier = current()/@identifier]) = 1">
        Variable '<sch:value-of select="@identifier"/>' is declared more than once.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item//qti:*[self::qti:qti-simple-choice or self::qti:qti-inline-choice or self::qti:qti-hottext
                       or self::qti:qti-hotspot-choice or self::qti:qti-simple-associable-choice or self::qti:qti-associable-hotspot
                       or self::qti:qti-gap-text or self::qti:qti-gap-img or self::qti:qti-gap]">
      <sch:let name="interaction" value="ancestor::qti:*[substring(local-name(), string-length(local-name()) - 11) = '-interaction'][1]"/>
      <sch:assert test="count($interaction//qti:*[self::qti:qti-simple-choice or self::qti:qti-inline-choice or self::qti:qti-hottext
                        or self::qti:qti-hotspot-choice or self::qti:qti-simple-associable-choice or self::qti:qti-associable-hotspot
                        or self::qti:qti-gap-text or self::qti:qti-gap-img or self::qti:qti-gap][@identifier = current()/@identifier]) &lt;= 1">
        Choice identifier '<sch:value-of select="@identifier"/>' is used by more than one choice in the same interaction.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    "The identifier of the section or item reference must be unique within
    the test and must not be the identifier of any testPart."
  -->
  <sch:pattern id="test-identifier-unique">
    <sch:rule context="qti:qti-assessment-test//qti:*[self::qti:qti-test-part or self::qti:qti-assessment-section
                       or self::qti:qti-assessment-section-ref or self::qti:qti-assessment-item-ref]">
      <sch:assert test="count(//qti:*[self::qti:qti-test-part or self::qti:qti-assessment-section or self::qti:qti-assessment-section-ref
                        or self::qti:qti-assessment-item-ref][@identifier = current()/@identifier]) = 1">
        Identifier '<sch:value-of select="@identifier"/>' is used by more than one test part, section or item reference in the test.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    "In the case of an item or section, the target must refer to an item or
    section in the same test-part that has not yet been presented. For
    test-parts, the target must refer to another test-part." The EXIT_
    values are the special targets. A test part that includes sections from
    other files is not checked.
  -->
  <sch:pattern id="branch-target-exists">
    <sch:rule context="qti:qti-test-part/qti:qti-branch-rule">
      <sch:assert test="@target = 'EXIT_TEST' or @target = 'EXIT_TESTPART' or @target = 'EXIT_SECTION'
                        or //qti:qti-test-part[@identifier = current()/@target][generate-id() != generate-id(current()/..)]">
        Branch rule target '<sch:value-of select="@target"/>' is not another test part of the test, nor EXIT_TEST.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-section/qti:qti-branch-rule | qti:qti-assessment-item-ref/qti:qti-branch-rule">
      <sch:assert test="@target = 'EXIT_SECTION' or @target = 'EXIT_TESTPART' or @target = 'EXIT_TEST'
                        or ancestor::qti:qti-test-part[1]//qti:qti-assessment-section-ref
                        or ../following::qti:*[self::qti:qti-assessment-section or self::qti:qti-assessment-item-ref]
                           [@identifier = current()/@target]
                           [generate-id(ancestor::qti:qti-test-part[1]) = generate-id(current()/ancestor::qti:qti-test-part[1])]">
        Branch rule target '<sch:value-of select="@target"/>' is not an item or section later in the same test part, nor EXIT_SECTION, EXIT_TESTPART or EXIT_TEST.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    In a test, a variable named "X.VAR" refers to variable VAR of item
    reference, section or test part X. Whether the item declares VAR needs
    the item's file and is not checked here. A test that includes sections
    from other files is not checked.
  -->
  <sch:pattern id="test-item-variable">
    <sch:rule context="qti:qti-assessment-test//qti:qti-variable[contains(@identifier, '.') and not(@identifier = //qti:qti-outcome-declaration/@identifier)]">
      <sch:assert test="//qti:qti-assessment-section-ref
                        or substring-before(@identifier, '.') = //qti:*[self::qti:qti-assessment-item-ref or self::qti:qti-assessment-section or self::qti:qti-test-part]/@identifier">
        <sch:name/> refers to '<sch:value-of select="@identifier"/>', but the test has no item reference, section or test part '<sch:value-of select="substring-before(@identifier, '.')"/>'.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    The standard response processing templates need specific variables:
    "A response variable called RESPONSE must have been declared and have an
    associated correct value. Similarly, the outcome variable SCORE must
    also have been declared." (match_correct); "Both variables must have
    been declared and RESPONSE must have an associated mapping"
    (map_response); "Both variables must been declared and RESPONSE must
    have base-type point" (map_response_point, which uses the area
    mapping). The Common Cartridge variants are the same templates. A
    template is known by its name after "/rptemplates/", so both the
    https://purl.imsglobal.org/spec/qti/v3p0/rptemplates/ URLs of the
    specification and the www.imsglobal.org/question/qti_v3p0/rptemplates/
    URLs in 1EdTech's examples count. A correct response set by template
    processing counts as a correct response.

    A template the specification does not define, without a
    template-location to fetch it from, cannot be run by a delivery engine
    that does not know it: a warning.
  -->
  <sch:pattern id="response-processing-template">
    <sch:rule context="qti:qti-assessment-item/qti:qti-response-processing[normalize-space(@template)]">
      <sch:let name="name" value="substring-before(concat(substring-after(@template, '/rptemplates/'), '.xml'), '.xml')"/>
      <sch:let name="match" value="$name = 'match_correct' or $name = 'CC2_match' or $name = 'CC2_match_basic'"/>
      <sch:let name="map" value="$name = 'map_response' or $name = 'CC2_map_response'"/>
      <sch:let name="point" value="$name = 'map_response_point'"/>
      <sch:let name="response" value="//qti:qti-response-declaration[@identifier = 'RESPONSE']"/>
      <sch:assert test="not($match or $map or $point) or $response">
        Response processing template '<sch:value-of select="$name"/>' needs a response variable RESPONSE, which is not declared.
      </sch:assert>
      <sch:assert test="not($match or $map or $point) or //qti:qti-outcome-declaration[@identifier = 'SCORE']">
        Response processing template '<sch:value-of select="$name"/>' needs an outcome variable SCORE, which is not declared.
      </sch:assert>
      <sch:assert test="not($match and $response) or $response/qti:qti-correct-response
                        or //qti:qti-template-processing//qti:qti-set-correct-response[@identifier = 'RESPONSE']">
        Response processing template '<sch:value-of select="$name"/>' needs a correct response for RESPONSE.
      </sch:assert>
      <sch:assert test="not($map and $response) or $response/qti:qti-mapping">
        Response processing template '<sch:value-of select="$name"/>' needs a qti-mapping for RESPONSE.
      </sch:assert>
      <sch:assert test="not($point and $response) or ($response/@base-type = 'point' and $response/qti:qti-area-mapping)">
        Response processing template 'map_response_point' needs RESPONSE with base-type point and a qti-area-mapping.
      </sch:assert>
      <sch:assert role="warning" test="$match or $map or $point or @template-location">
        Response processing template '<sch:value-of select="@template"/>' is not defined by QTI 3 and has no template-location.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    qti-map-response "looks up the value of a response variable and then
    transforms it using the associated mapping, which must have been
    declared"; qti-map-response-point needs "a response variable that must
    be of base-type point" and its area mapping.
  -->
  <sch:pattern id="mapping-declared">
    <sch:rule context="qti:qti-map-response">
      <sch:let name="decl" value="//qti:qti-response-declaration[@identifier = current()/@identifier]"/>
      <sch:assert test="not($decl) or $decl/qti:qti-mapping">
        qti-map-response uses response variable '<sch:value-of select="@identifier"/>', which has no qti-mapping.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-map-response-point">
      <sch:let name="decl" value="//qti:qti-response-declaration[@identifier = current()/@identifier]"/>
      <sch:assert test="not($decl) or ($decl/@base-type = 'point' and $decl/qti:qti-area-mapping)">
        qti-map-response-point uses response variable '<sch:value-of select="@identifier"/>', which must have base-type point and a qti-area-mapping.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    "An expression used in a qti-template-rule must not refer to the value
    of a response variable or outcome variable."
  -->
  <sch:pattern id="template-processing-scope">
    <sch:rule context="qti:qti-template-processing//qti:*[self::qti:qti-variable or self::qti:qti-map-response or self::qti:qti-map-response-point]">
      <sch:assert test="not(@identifier = $responses or @identifier = $outcomes
                        or @identifier = 'numAttempts' or @identifier = 'duration' or @identifier = 'completionStatus')">
        Template processing must not use the value of response or outcome variable '<sch:value-of select="@identifier"/>'.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    Each interaction "must be bound to a response variable with" a given
    base-type and cardinality, as the specification states per interaction.
    Each rule names them in $base-types and $cardinalities; an empty
    $base-types leaves the base-type free. A record response has no
    base-type.
  -->
  <sch:pattern id="interaction-response-type">
    <sch:rule abstract="true" id="binding">
      <sch:let name="decl" value="//qti:qti-response-declaration[@identifier = current()/@response-identifier]"/>
      <sch:assert test="not($decl) or contains($cardinalities, concat(' ', $decl/@cardinality, ' '))">
        <sch:name/> must be bound to a response variable with cardinality<sch:value-of select="$cardinalities"/>but '<sch:value-of select="@response-identifier"/>' has cardinality <sch:value-of select="$decl/@cardinality"/>.
      </sch:assert>
      <sch:assert test="not($decl) or $base-types = '' or $decl/@cardinality = 'record' or contains($base-types, concat(' ', $decl/@base-type, ' '))">
        <sch:name/> must be bound to a response variable with base-type<sch:value-of select="$base-types"/>but '<sch:value-of select="@response-identifier"/>' has base-type <sch:value-of select="$decl/@base-type"/>.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-choice-interaction | qti:qti-hottext-interaction | qti:qti-hotspot-interaction">
      <sch:let name="base-types" value="' identifier '"/>
      <sch:let name="cardinalities" value="' single multiple '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-order-interaction | qti:qti-graphic-order-interaction">
      <sch:let name="base-types" value="' identifier '"/>
      <sch:let name="cardinalities" value="' ordered '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-inline-choice-interaction">
      <sch:let name="base-types" value="' identifier '"/>
      <sch:let name="cardinalities" value="' single '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-associate-interaction | qti:qti-graphic-associate-interaction">
      <sch:let name="base-types" value="' pair '"/>
      <sch:let name="cardinalities" value="' single multiple '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-match-interaction | qti:qti-gap-match-interaction">
      <sch:let name="base-types" value="' directedPair '"/>
      <sch:let name="cardinalities" value="' single multiple '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-graphic-gap-match-interaction">
      <sch:let name="base-types" value="' directedPair '"/>
      <sch:let name="cardinalities" value="' multiple '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-select-point-interaction | qti:qti-position-object-interaction">
      <sch:let name="base-types" value="' point '"/>
      <sch:let name="cardinalities" value="' single multiple '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-text-entry-interaction">
      <sch:let name="base-types" value="''"/>
      <sch:let name="cardinalities" value="' single record '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-extended-text-interaction">
      <sch:let name="base-types" value="''"/>
      <sch:let name="cardinalities" value="' single multiple ordered record '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-slider-interaction">
      <sch:let name="base-types" value="' integer float '"/>
      <sch:let name="cardinalities" value="' single '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-media-interaction">
      <sch:let name="base-types" value="' integer '"/>
      <sch:let name="cardinalities" value="' single '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-upload-interaction | qti:qti-drawing-interaction">
      <sch:let name="base-types" value="' file '"/>
      <sch:let name="cardinalities" value="' single '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
    <sch:rule context="qti:qti-end-attempt-interaction">
      <sch:let name="base-types" value="' boolean '"/>
      <sch:let name="cardinalities" value="' single '"/>
      <sch:extends rule="binding"/>
    </sch:rule>
  </sch:pattern>

  <!--
    A correct response or map key that names no choice of the interaction
    can never be given by the candidate. Checked only for responses whose
    interactions all offer fixed choices. A warning: the specification does
    not forbid it in so many words.
  -->
  <sch:pattern id="choice-value-exists">
    <sch:rule context="qti:qti-assessment-item/qti:qti-response-declaration[@base-type = 'identifier' or @base-type = 'pair' or @base-type = 'directedPair']">
      <sch:let name="bound" value="//qti:*[@response-identifier = current()/@identifier]"/>
      <sch:let name="fixed" value="count($bound) > 0 and count($bound[self::qti:qti-choice-interaction or self::qti:qti-order-interaction
                                   or self::qti:qti-inline-choice-interaction or self::qti:qti-hottext-interaction
                                   or self::qti:qti-hotspot-interaction or self::qti:qti-graphic-order-interaction
                                   or self::qti:qti-associate-interaction or self::qti:qti-graphic-associate-interaction
                                   or self::qti:qti-match-interaction or self::qti:qti-gap-match-interaction
                                   or self::qti:qti-graphic-gap-match-interaction]) = count($bound)"/>
      <sch:let name="choices" value="$bound//qti:*[self::qti:qti-simple-choice or self::qti:qti-inline-choice or self::qti:qti-hottext
                                     or self::qti:qti-hotspot-choice or self::qti:qti-simple-associable-choice or self::qti:qti-associable-hotspot
                                     or self::qti:qti-gap-text or self::qti:qti-gap-img or self::qti:qti-gap]/@identifier"/>
      <sch:let name="single" value="@base-type = 'identifier'"/>
      <!-- Values that name no choice: a whole identifier, or either half of a pair. -->
      <sch:let name="bad-values" value="qti:qti-correct-response/qti:qti-value[($single and not(normalize-space(.) = $choices))
                                        or (not($single) and not(substring-before(normalize-space(.), ' ') = $choices and substring-after(normalize-space(.), ' ') = $choices))]"/>
      <sch:let name="bad-keys" value="qti:qti-mapping/qti:qti-map-entry[($single and not(normalize-space(@map-key) = $choices))
                                      or (not($single) and not(substring-before(normalize-space(@map-key), ' ') = $choices and substring-after(normalize-space(@map-key), ' ') = $choices))]"/>
      <sch:assert role="warning" test="not($fixed) or not($bad-values)">
        The correct response of '<sch:value-of select="@identifier"/>' names '<sch:value-of select="normalize-space($bad-values[1])"/>', which is not a choice of its interaction.
      </sch:assert>
      <sch:assert role="warning" test="not($fixed) or not($bad-keys)">
        The mapping of '<sch:value-of select="@identifier"/>' has key '<sch:value-of select="normalize-space($bad-keys[1]/@map-key)"/>', which is not a choice of its interaction.
      </sch:assert>
    </sch:rule>
  </sch:pattern>

  <!--
    "Outcome variables are only relevant within the parent Item and so every
    declared variable must be referenced in the corresponding outcomes
    processing", and the same for template variables and template
    processing. The item works without, so this is a warning. A standard
    response processing template references SCORE. An item without response
    processing, such as one that is scored by hand, is not checked: having
    no scoring is allowed.
  -->
  <sch:pattern id="variable-used">
    <sch:rule context="qti:qti-assessment-item[qti:qti-response-processing/@template or qti:qti-response-processing/*]/qti:qti-outcome-declaration">
      <sch:assert role="warning" test="../qti:qti-response-processing//qti:*[@identifier = current()/@identifier]
                                       or (@identifier = 'SCORE' and ../qti:qti-response-processing/@template)">
        Outcome variable '<sch:value-of select="@identifier"/>' is declared but not used in response processing.
      </sch:assert>
    </sch:rule>
    <sch:rule context="qti:qti-assessment-item[qti:qti-template-processing/*]/qti:qti-template-declaration">
      <sch:assert role="warning" test="../qti:qti-template-processing//qti:*[@identifier = current()/@identifier]">
        Template variable '<sch:value-of select="@identifier"/>' is declared but not used in template processing.
      </sch:assert>
    </sch:rule>
  </sch:pattern>
</sch:schema>
