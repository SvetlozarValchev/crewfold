package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"crewfold/internal/localapi"
	protocolschema "crewfold/protocol"
)

// operatorRequestParamContracts is the executable current-contract registry
// for every request the operator TUI can issue. Validation runs on raw JSON so
// omitted fields, explicit nulls, and explicit zero values remain distinct.
var operatorRequestParamContracts = map[string]string{
	localapi.MethodWorkspaceShow:        "local/v1/workspace-show.params.schema.json",
	localapi.MethodProjectShow:          "local/v1/project-show.params.schema.json",
	localapi.MethodAgentShow:            "local/v1/agent-query.params.schema.json",
	localapi.MethodWorkspaceList:        "local/v1/workspace-list.params.schema.json",
	localapi.MethodProjectList:          "local/v1/project-list.params.schema.json",
	localapi.MethodAgentList:            "local/v1/agent-list.params.schema.json",
	localapi.MethodObjectiveList:        "local/v1/objective-list.params.schema.json",
	localapi.MethodTaskList:             "local/v1/task-list.params.schema.json",
	localapi.MethodRunList:              "local/v1/run-list.params.schema.json",
	localapi.MethodClaimList:            "local/v1/claim-list.params.schema.json",
	localapi.MethodOverlapList:          "local/v1/overlap-list.params.schema.json",
	localapi.MethodDriftList:            "local/v1/drift-list.params.schema.json",
	localapi.MethodMeetingList:          "local/v1/meeting-list.params.schema.json",
	localapi.MethodApprovalList:         "local/v1/approval-list.params.schema.json",
	localapi.MethodCheckList:            "local/v1/check-list.params.schema.json",
	localapi.MethodInboxList:            "local/v1/inbox-list.params.schema.json",
	localapi.MethodEventsList:           "local/v1/events-list.params.schema.json",
	localapi.MethodEventsTimeline:       "local/v1/events-timeline.params.schema.json",
	localapi.MethodBriefingShow:         "local/v1/briefing-show.params.schema.json",
	localapi.MethodBriefingExplain:      "local/v1/briefing-explain.params.schema.json",
	localapi.MethodSupervisorActionShow: "local/v1/supervisor-action-query.params.schema.json",
	localapi.MethodRunAttach:            "local/v1/run-attach.params.schema.json",
	localapi.MethodRunResume:            "local/v1/run-resume.params.schema.json",
	localapi.MethodRunStop:              "local/v1/run-stop.params.schema.json",
	localapi.MethodRunLostResolve:       "local/v1/run-lost-resolve.params.schema.json",
	localapi.MethodApprovalAllow:        "local/v1/approval-decision.params.schema.json",
	localapi.MethodApprovalDeny:         "local/v1/approval-decision.params.schema.json",
	localapi.MethodSystemDoctorFull:     "local/v1/system-doctor-full.params.schema.json",
	localapi.MethodBackupCreate:         "local/v1/backup-create.params.schema.json",
}

func validateOperatorRequestParams(request localapi.Request) error {
	schemaPath, ok := operatorRequestParamContracts[request.Method]
	if !ok {
		return nil
	}
	if err := protocolschema.ValidateJSON(schemaPath, request.Params); err != nil {
		return fmt.Errorf("%s requires parameters matching the current protocol schema: %w", request.Method, err)
	}
	return nil
}

func decodeLocalAPIRequest(data []byte, request *localapi.Request) error {
	if err := rejectDuplicateJSONFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains more than one JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanUniqueJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains more than one value")
		}
		return err
	}
	return nil
}

func scanUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object member name is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	_, err = decoder.Token()
	return err
}
