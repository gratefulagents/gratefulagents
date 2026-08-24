def execute(data):
    mismatch = data["deployed"] != data["declared"]
    return {"executed": True, "rejected": False, "executed_mismatch": mismatch}
