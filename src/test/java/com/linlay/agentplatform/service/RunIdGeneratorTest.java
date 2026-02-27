package com.linlay.agentplatform.service;

import org.junit.jupiter.api.Test;

import static org.assertj.core.api.Assertions.assertThat;

class RunIdGeneratorTest {

    @Test
    void encodeEpochMillisShouldUseBase36Lowercase() {
        assertThat(RunIdGenerator.encodeEpochMillis(35L)).isEqualTo("z");
        assertThat(RunIdGenerator.encodeEpochMillis(36L)).isEqualTo("10");
        assertThat(RunIdGenerator.encodeEpochMillis(1700000000000L))
                .isEqualTo(Long.toString(1700000000000L, 36));
    }

    @Test
    void nextRunIdShouldReturnBase36Timestamp() {
        String runId = RunIdGenerator.nextRunId();
        assertThat(runId).isNotBlank();
        assertThat(runId).matches("^[0-9a-z]+$");
    }
}
