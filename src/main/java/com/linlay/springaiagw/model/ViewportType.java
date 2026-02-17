package com.linlay.springaiagw.model;

public enum ViewportType {
    HTML("html"),
    QLC("qlc");

    private final String value;

    ViewportType(String value) {
        this.value = value;
    }

    public String value() {
        return value;
    }
}
