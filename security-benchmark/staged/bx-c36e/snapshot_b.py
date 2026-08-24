def execute(data):
    mismatch = data["deployed"] != data["declared"]
    return {"executed": not mismatch, "rejected": mismatch, "executed_mismatch": False}
