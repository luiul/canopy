from canopy.state import AgentState, classify_state


def test_classify_state_working_at_or_above_threshold():
    assert classify_state(pcpu=5.0, threshold=1.5) == AgentState.WORKING
    assert classify_state(pcpu=1.5, threshold=1.5) == AgentState.WORKING


def test_classify_state_idle_below_threshold():
    assert classify_state(pcpu=0.0, threshold=1.5) == AgentState.IDLE
    assert classify_state(pcpu=1.4, threshold=1.5) == AgentState.IDLE


def test_classify_state_unknown_when_pid_has_no_sample():
    assert classify_state(pcpu=None) == AgentState.UNKNOWN


def test_agent_state_values_are_plain_strings_for_display():
    assert AgentState.WORKING.value == "working"
    assert AgentState.IDLE.value == "idle"
    assert AgentState.UNKNOWN.value == "unknown"
