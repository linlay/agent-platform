package com.linlay.springaiagw.agent.mode;

final class PlanExecutionStalledException extends RuntimeException {

    PlanExecutionStalledException(String message) {
        super(message);
    }
}
