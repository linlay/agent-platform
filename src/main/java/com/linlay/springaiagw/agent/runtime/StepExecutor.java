package com.linlay.springaiagw.agent.runtime;

import com.linlay.springaiagw.model.agw.AgentDelta;

import java.util.List;

public interface StepExecutor {

    List<AgentDelta> execute(ExecutionContext context, String stepInstruction);
}
