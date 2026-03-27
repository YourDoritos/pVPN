package ipc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
)

func TestMarshalData(t *testing.T) {
	data := MarshalData(StatusData{State: "Connected", Server: "CH#1"})
	var sd StatusData
	if err := json.Unmarshal(data, &sd); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sd.State != "Connected" || sd.Server != "CH#1" {
		t.Errorf("got %+v", sd)
	}
}

func TestRequestJSON(t *testing.T) {
	req := Request{Command: "connect", Params: MarshalData(ConnectParams{Server: "CH#1"})}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Command != "connect" {
		t.Errorf("expected connect, got %s", decoded.Command)
	}
	var params ConnectParams
	json.Unmarshal(decoded.Params, &params)
	if params.Server != "CH#1" {
		t.Errorf("expected CH#1, got %s", params.Server)
	}
}

func TestResponseJSON(t *testing.T) {
	resp := Response{OK: true, Data: MarshalData(StatusData{State: "disconnected"})}
	data, _ := json.Marshal(resp)
	var decoded Response
	json.Unmarshal(data, &decoded)
	if !decoded.OK {
		t.Error("expected OK=true")
	}
}

func TestResponseError(t *testing.T) {
	resp := Response{OK: false, Error: "not found"}
	data, _ := json.Marshal(resp)
	var decoded Response
	json.Unmarshal(data, &decoded)
	if decoded.OK || decoded.Error != "not found" {
		t.Errorf("got %+v", decoded)
	}
}

func TestEventJSON(t *testing.T) {
	evt := Event{Type: "state-changed", Data: MarshalData(StateChangedData{
		State: "Connected", Server: "DE#1", Country: "DE",
	})}
	data, _ := json.Marshal(evt)
	var decoded Event
	json.Unmarshal(data, &decoded)
	if decoded.Type != "state-changed" {
		t.Errorf("got type %s", decoded.Type)
	}
	var sd StateChangedData
	json.Unmarshal(decoded.Data, &sd)
	if sd.State != "Connected" || sd.Server != "DE#1" {
		t.Errorf("got %+v", sd)
	}
}

func TestWriteReadJSON(t *testing.T) {
	var buf bytes.Buffer
	req := Request{Command: "status"}
	if err := WriteJSON(&buf, &req); err != nil {
		t.Fatal(err)
	}
	// Should end with newline
	if buf.Bytes()[buf.Len()-1] != '\n' {
		t.Error("expected trailing newline")
	}

	reader := bufio.NewReader(&buf)
	var decoded Request
	if err := ReadJSON(reader, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Command != "status" {
		t.Errorf("expected status, got %s", decoded.Command)
	}
}

func TestStatusDataRoundTrip(t *testing.T) {
	original := StatusData{
		State: "Connected", Server: "US#1", ServerIP: "1.2.3.4",
		Country: "US", Protocol: "wireguard", Duration: 120,
		RxBytes: 1000, TxBytes: 2000, ForwardedPort: 54321,
		Username: "user@example.com", PlanName: "Plus",
	}
	data := MarshalData(original)
	var decoded StatusData
	json.Unmarshal(data, &decoded)
	if decoded != original {
		t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestStatsUpdateDataRoundTrip(t *testing.T) {
	original := StatsUpdateData{RxBytes: 12345, TxBytes: 67890, Handshake: 1700000000}
	data := MarshalData(original)
	var decoded StatsUpdateData
	json.Unmarshal(data, &decoded)
	if decoded != original {
		t.Errorf("mismatch: got %+v", decoded)
	}
}
