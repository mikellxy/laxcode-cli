from typing import TypedDict
from langgraph.graph import StateGraph, START, END
from langgraph.checkpoint.memory import MemorySaver
import random
import argparse
import subprocess
import json

parser = argparse.ArgumentParser()
parser.add_argument("-session", type=str, required=True)
parser.add_argument("-task", type=str, required=True)
parser.add_argument("-workdir", type=str, required=True)
args = parser.parse_args()

random.seed()
exec_times = 0

class WorkState(TypedDict):
    user_query: str
    plan: str
    result: str
    exec_times: int

def input_node(state: WorkState):
    print("===input_node===")
    return {"user_query": state["user_query"]}

def execute_node(state: WorkState):
    print("===execute_node===")
    global exec_times
    exec_times += 1

    result = subprocess.run(
        ["./bin/laxcode", "-workdir", args.workdir, "-oneshot", "-session", args.session, "-task", state["user_query"]],
        capture_output=True,
        text=True,
        timeout=120
    )
    stdout = result.stdout
    stderr = result.stderr
    exit_code = result.returncode
    if exit_code != 0:
        raise RuntimeError(f"call laxcode cli failed,exit:{exit_code},stderr:{stderr}")

    stdout_data = json.loads(stdout)

    return {"result": stdout_data['result'], "exec_times": exec_times}

# Node3:汇总
def summary_node(state: WorkState):
    print("===summary_node===")
    print(state["result"])
    print("exec_times: ", exec_times)
    return state

builder = StateGraph(WorkState)
builder.add_node("input", input_node)
builder.add_node("execute", execute_node)
builder.add_node("summary", summary_node)

builder.add_edge(START, "input")
builder.add_edge("input", "execute")
builder.add_edge("execute", "summary")
builder.add_edge("summary", END)

memory = MemorySaver()
graph = builder.compile(checkpointer=memory)

thread_config = {"configurable": {"thread_id": "workflow-agent-hybrid-example"}}
try:
    res = graph.invoke({"user_query": args.task, "exec_times": exec_times}, config=thread_config)
except:
    result = graph.invoke(None, config=thread_config)
