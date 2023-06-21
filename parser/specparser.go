/*----------------------------------------------------------------
 *  Copyright (c) ThoughtWorks, Inc.
 *  Licensed under the Apache License, Version 2.0
 *  See LICENSE in the project root for license information.
 *----------------------------------------------------------------*/

package parser

import (
	"bufio"
	"sort"
	"strconv"
	"strings"

	"github.com/getgauge/gauge/gauge"
	"github.com/getgauge/gauge/logger"
)

// SpecParser is responsible for parsing a Specification. It delegates to respective processors composed sub-entities
type SpecParser struct {
	scanner           *bufio.Scanner
	lineNo            int
	tokens            []*Token
	currentState      int
	processors        map[gauge.TokenKind]func(*SpecParser, *Token) ([]error, bool)
	conceptDictionary *gauge.ConceptDictionary
}

type PrioritizedScenarios struct {
	priority     int
	scenarioList []*gauge.Scenario
}

// Add filtering metods to PrioritizedScenarios list, using Sort
type ByPriority []*PrioritizedScenarios

func (a ByPriority) Len() int           { return len(a) }
func (a ByPriority) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByPriority) Less(i, j int) bool { return a[i].priority < a[j].priority }

// Parse generates tokens for the given spec text and creates the specification.
func (parser *SpecParser) Parse(specText string, conceptDictionary *gauge.ConceptDictionary, specFile string) (*gauge.Specification, *ParseResult, error) {
	tokens, errs := parser.GenerateTokens(specText, specFile)
	spec, res, err := parser.CreateSpecification(tokens, conceptDictionary, specFile)
	if err != nil {
		return nil, nil, err
	}
	res.FileName = specFile
	if len(errs) > 0 {
		res.Ok = false
	}
	res.ParseErrors = append(errs, res.ParseErrors...)
	return spec, res, nil
}

// ParseSpecText without validating and replacing concepts.
func (parser *SpecParser) ParseSpecText(specText string, specFile string) (*gauge.Specification, *ParseResult) {
	tokens, errs := parser.GenerateTokens(specText, specFile)
	spec, res := parser.createSpecification(tokens, specFile)
	res.FileName = specFile
	if len(errs) > 0 {
		res.Ok = false
	}
	res.ParseErrors = append(errs, res.ParseErrors...)
	return spec, res
}

// CreateSpecification creates specification from the given set of tokens.
func (parser *SpecParser) CreateSpecification(tokens []*Token, conceptDictionary *gauge.ConceptDictionary, specFile string) (*gauge.Specification, *ParseResult, error) {
	parser.conceptDictionary = conceptDictionary
	specification, finalResult := parser.createSpecification(tokens, specFile)
	if err := specification.ProcessConceptStepsFrom(conceptDictionary); err != nil {
		return nil, nil, err
	}
	err := parser.validateSpec(specification)
	if err != nil {
		finalResult.Ok = false
		finalResult.ParseErrors = append([]ParseError{err.(ParseError)}, finalResult.ParseErrors...)
	}
	return specification, finalResult, nil
}

func (parser *SpecParser) createSpecification(tokens []*Token, specFile string) (*gauge.Specification, *ParseResult) {
	finalResult := &ParseResult{ParseErrors: make([]ParseError, 0), Ok: true}
	converters := parser.initializeConverters()
	specification := &gauge.Specification{FileName: specFile}
	state := initial
	for _, token := range tokens {
		for _, converter := range converters {
			result := converter(token, &state, specification)
			if !result.Ok {
				if result.ParseErrors != nil {
					finalResult.Ok = false
					finalResult.ParseErrors = append(finalResult.ParseErrors, result.ParseErrors...)
				}
			}
			if result.Warnings != nil {
				if finalResult.Warnings == nil {
					finalResult.Warnings = make([]*Warning, 0)
				}
				finalResult.Warnings = append(finalResult.Warnings, result.Warnings...)
			}
		}
	}
	// For each priority flag we find, we should create a scenario list associated to this priority level, these lists are pushed in prioritizedScenariosList
	// On the other side, we fill nonPrioritizedScenarios with the scenarios without priority flag
	prioritizedScenariosList := []*PrioritizedScenarios{}
	nonPrioritizedScenarios := []*gauge.Scenario{}
	for _, scenario := range specification.Scenarios {
		scenarioPriority := -1
		// We look for scenarios with priority level tags
		for _, tag := range scenario.Tags.RawValues[0] {
			if strings.Contains(tag, "Priority") {
				priority, err := strconv.Atoi(strings.SplitAfter(tag, "Priority")[1])
				if err != nil {
					logger.Warningf(true, "Unable to get priority level from tag: %s", tag)
					break
				}
				if priority >= 0 {
					logger.Debugf(true, "Scenario: %s has Priority level: %d", scenario.Heading.Value, priority)
					if scenarioPriority == -1 {
						// If not priority level has been set before to this scenario, we should do it now
						scenarioPriority = priority
					} else if priority < scenarioPriority {
						// By default we stick to the highest priority level
						scenarioPriority = priority
					}
				}
			}
		}
		if scenarioPriority != -1 {
			// Push this scenario to its associated scenario list, if the list exists
			prioritizedScenariosFound := false
			for _, prioritizedScenarios := range prioritizedScenariosList {
				if prioritizedScenarios.priority == scenarioPriority {
					prioritizedScenariosFound = true
					prioritizedScenarios.scenarioList = append(prioritizedScenarios.scenarioList, scenario)
					break
				}
			}
			if !prioritizedScenariosFound {
				// Create the prioritized list, if the list does not exist
				prioritizedScenarios := new(PrioritizedScenarios)
				prioritizedScenarios.priority = scenarioPriority
				prioritizedScenarios.scenarioList = append(prioritizedScenarios.scenarioList, scenario)
				prioritizedScenariosList = append(prioritizedScenariosList, prioritizedScenarios)
			}

		} else { // Add this scenario to nonPrioritizedScenarios if not priority flag has been found
			nonPrioritizedScenarios = append(nonPrioritizedScenarios, scenario)
		}
	}
	// Filter list of list of Scenarios by priority level
	sort.Sort(ByPriority(prioritizedScenariosList))
	// We create a brand new, empty scenario list for the specification
	specification.Scenarios = []*gauge.Scenario{}
	for _, prioritizedScenarios := range prioritizedScenariosList {
		// Fill the specification scenario list, starting with the prioritized ones
		// Note: Priority levels are respected because the list has been sorted by priority level
		specification.Scenarios = append(specification.Scenarios, prioritizedScenarios.scenarioList...)
	}
	// Append nonPrioritizedScenarios to this list
	specification.Scenarios = append(specification.Scenarios, nonPrioritizedScenarios...)
	if len(specification.Scenarios) > 0 {
		specification.LatestScenario().Span.End = tokens[len(tokens)-1].LineNo
	}
	return specification, finalResult
}

func (parser *SpecParser) validateSpec(specification *gauge.Specification) error {
	if len(specification.Items) == 0 {
		specification.AddHeading(&gauge.Heading{})
		return ParseError{FileName: specification.FileName, LineNo: 1, SpanEnd: 1, Message: "Spec does not have any elements"}
	}
	if specification.Heading == nil {
		specification.AddHeading(&gauge.Heading{})
		return ParseError{FileName: specification.FileName, LineNo: 1, SpanEnd: 1, Message: "Spec heading not found"}
	}
	if len(strings.TrimSpace(specification.Heading.Value)) < 1 {
		return ParseError{FileName: specification.FileName, LineNo: specification.Heading.LineNo, SpanEnd: specification.Heading.LineNo, Message: "Spec heading should have at least one character"}
	}

	dataTable := specification.DataTable.Table
	if dataTable.IsInitialized() && dataTable.GetRowCount() == 0 {
		return ParseError{FileName: specification.FileName, LineNo: dataTable.LineNo, SpanEnd: dataTable.LineNo, Message: "Data table should have at least 1 data row"}
	}
	if len(specification.Scenarios) == 0 {
		return ParseError{FileName: specification.FileName, LineNo: specification.Heading.LineNo, SpanEnd: specification.Heading.SpanEnd, Message: "Spec should have atleast one scenario"}
	}
	for _, sce := range specification.Scenarios {
		if len(sce.Steps) == 0 {
			return ParseError{FileName: specification.FileName, LineNo: sce.Heading.LineNo, SpanEnd: sce.Heading.SpanEnd, Message: "Scenario should have atleast one step"}
		}
	}
	return nil
}

func createStep(spec *gauge.Specification, scn *gauge.Scenario, stepToken *Token) (*gauge.Step, *ParseResult) {
	tables := []*gauge.Table{spec.DataTable.Table}
	if scn != nil {
		tables = append(tables, scn.DataTable.Table)
	}
	dataTableLookup := new(gauge.ArgLookup).FromDataTables(tables...)
	stepToAdd, parseDetails := CreateStepUsingLookup(stepToken, dataTableLookup, spec.FileName)
	if stepToAdd != nil {
		stepToAdd.Suffix = stepToken.Suffix
	}
	return stepToAdd, parseDetails
}

// CreateStepUsingLookup generates gauge steps from step token and args lookup.
func CreateStepUsingLookup(stepToken *Token, lookup *gauge.ArgLookup, specFileName string) (*gauge.Step, *ParseResult) {
	stepValue, argsType := extractStepValueAndParameterTypes(stepToken.Value)
	if argsType != nil && len(argsType) != len(stepToken.Args) {
		return nil, &ParseResult{ParseErrors: []ParseError{ParseError{specFileName, stepToken.LineNo, stepToken.SpanEnd, "Step text should not have '{static}' or '{dynamic}' or '{special}'", stepToken.LineText()}}, Warnings: nil}
	}
	lineText := strings.Join(stepToken.Lines, " ")
	step := &gauge.Step{FileName: specFileName, LineNo: stepToken.LineNo, Value: stepValue, LineText: strings.TrimSpace(lineText), LineSpanEnd: stepToken.SpanEnd}
	arguments := make([]*gauge.StepArg, 0)
	var errors []ParseError
	var warnings []*Warning
	for i, argType := range argsType {
		argument, parseDetails := createStepArg(stepToken.Args[i], argType, stepToken, lookup, specFileName)
		if parseDetails != nil && len(parseDetails.ParseErrors) > 0 {
			errors = append(errors, parseDetails.ParseErrors...)
		}
		arguments = append(arguments, argument)
		if parseDetails != nil && parseDetails.Warnings != nil {
			warnings = append(warnings, parseDetails.Warnings...)
		}
	}
	step.AddArgs(arguments...)
	return step, &ParseResult{ParseErrors: errors, Warnings: warnings}
}
