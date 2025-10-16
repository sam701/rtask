#!/bin/bash
# Task that outputs to both stdout and stderr
echo "This is stdout"
echo "This is stderr" >&2
exit 0
